/*
Copyright 2021 The Stupig Authors.
Copyright 2020 The Kruise Authors.
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package atomic

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/ppltools/webhookmanager/log"
)

const (
	maxFileNameLength = 255
	maxPathLength     = 4096
)

// Writer handles atomically projecting content for a set of files into
// a target directory.
//
// Note:
//
// 1. Writer reserves the set of pathnames starting with `..`.
// 2. Writer offers no concurrency guarantees and must be synchronized
//    by the caller.
//
// The visible files in this volume are symlinks to files in the writer's data
// directory.  Actual files are stored in a hidden timestamped directory which
// is symlinked to by the data directory. The timestamped directory and
// data directory symlink are created in the writer's target dir.  This scheme
// allows the files to be atomically updated by changing the target of the
// data directory symlink.
//
// Consumers of the target directory can monitor the ..data symlink using
// inotify or fanotify to receive events when the content in the volume is
// updated.
type Writer struct {
	targetDir string
}

type FileProjection struct {
	Data []byte
	Mode int32
}

// NewAtomicWriter creates a new Writer configured to write to the given
// target directory, or returns an error if the target directory does not exist.
func NewAtomicWriter(targetDir string) (*Writer, error) {
	_, err := os.Stat(targetDir)
	if os.IsNotExist(err) {
		return nil, err
	}

	return &Writer{targetDir: targetDir}, nil
}

const (
	dataDirName    = "..data"
	newDataDirName = "..data_tmp"
)

// Write does an atomic projection of the given payload into the writer's target
// directory.  Input paths must not begin with '..'.
//
// The Write algorithm is:
//
//  1.  The payload is validated; if the payload is invalid, the function returns
//  2.  The current timestamped directory is detected by reading the data directory
//      symlink
//  3.  The old version of the volume is walked to determine whether any
//      portion of the payload was deleted and is still present on disk.
//  4.  The data in the current timestamped directory is compared to the projected
//      data to determine if an update is required.
//  5.  A new timestamped dir is created
//  6.  The payload is written to the new timestamped directory
//  7.  Symlinks and directory for new user-visible files are created (if needed).
//
//      For example, consider the files:
//        <target-dir>/podName
//        <target-dir>/user/labels
//        <target-dir>/k8s/annotations
//
//      The user visible files are symbolic links into the internal data directory:
//        <target-dir>/podName         -> ..data/podName
//        <target-dir>/usr -> ..data/usr
//        <target-dir>/k8s -> ..data/k8s
//
//      The data directory itself is a link to a timestamped directory with
//      the real data:
//        <target-dir>/..data          -> ..2016_02_01_15_04_05.12345678/
//  8.  A symlink to the new timestamped directory ..data_tmp is created that will
//      become the new data directory
//  9.  The new data directory symlink is renamed to the data directory; rename is atomic
// 10.  Old paths are removed from the user-visible portion of the target directory
// 11.  The previous timestamped directory is removed, if it exists
func (w *Writer) Write(payload map[string]FileProjection) error {
	// (1)
	cleanPayload, err := validatePayload(payload)
	if err != nil {
		log.Errorf(context.Background(), "validate payload failed: %s", err)
		return err
	}

	// (2)
	dataDirPath := path.Join(w.targetDir, dataDirName)
	oldTsDir, err := os.Readlink(dataDirPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Errorf(context.Background(), "read link for data directory failed: %s", err)
			return err
		}
		// although Readlink() returns "" on err, don't be fragile by relying on it (since it's not specified in docs)
		// empty oldTsDir indicates that it didn't exist
		oldTsDir = ""
	}
	oldTsPath := path.Join(w.targetDir, oldTsDir)

	var pathsToRemove sets.String
	// if there was no old version, there's nothing to remove
	if len(oldTsDir) != 0 {
		// (3)
		pathsToRemove, err = w.pathsToRemove(cleanPayload, oldTsPath)
		if err != nil {
			log.Errorf(context.Background(), "determine user-visible files to remove failed: %s", err)
			return err
		}

		// (4)
		if should, err := shouldWritePayload(cleanPayload, oldTsPath); err != nil {
			log.Errorf(context.Background(), "determine whether payload should be written to disk failed: %s", err)
			return err
		} else if !should && len(pathsToRemove) == 0 {
			return nil
		} else {
			log.Infof(context.Background(), "write required for target directory: %s", w.targetDir)
		}
	}

	// (5)
	tsDir, err := w.newTimestampDir()
	if err != nil {
		log.Errorf(context.Background(), "creating new ts data directory failed: %s", err)
		return err
	}
	tsDirName := filepath.Base(tsDir)

	// (6)
	if err = w.writePayloadToDir(cleanPayload, tsDir); err != nil {
		log.Errorf(context.Background(), "write payload to ts data directory %s failed: %s", tsDir, err)
		return err
	}
	log.Infof(context.Background(), "performed write of new data to ts data directory %s", tsDir)

	// (7)
	if err = w.createUserVisibleFiles(cleanPayload); err != nil {
		log.Errorf(context.Background(), "create visible symlinks in target directory %s failed: %s", w.targetDir, err)
		return err
	}

	// (8)
	newDataDirPath := path.Join(w.targetDir, newDataDirName)
	if err = os.Symlink(tsDirName, newDataDirPath); err != nil {
		os.RemoveAll(tsDir)
		log.Errorf(context.Background(), "create symbolic link for atomic update failed: %s", err)
		return err
	}

	// (9)
	if runtime.GOOS == "windows" {
		os.Remove(dataDirPath)
		err = os.Symlink(tsDirName, dataDirPath)
		os.Remove(newDataDirPath)
	} else {
		err = os.Rename(newDataDirPath, dataDirPath)
	}
	if err != nil {
		os.Remove(newDataDirPath)
		os.RemoveAll(tsDir)
		log.Errorf(context.Background(), "rename symbolic link for data directory %s failed: %s", newDataDirPath, err)
		return err
	}

	// (10)
	if err = w.removeUserVisiblePaths(pathsToRemove); err != nil {
		log.Errorf(context.Background(), "remove old visible symlinks failed: %s", err)
		return err
	}

	// (11)
	if len(oldTsDir) > 0 {
		if err = os.RemoveAll(oldTsPath); err != nil {
			log.Errorf(context.Background(), "remove old data directory %s failed: %s", oldTsDir, err)
			return err
		}
	}

	return nil
}

// validatePayload returns an error if any path in the payload  returns a copy of the payload with the paths cleaned.
func validatePayload(payload map[string]FileProjection) (map[string]FileProjection, error) {
	cleanPayload := make(map[string]FileProjection)
	for k, content := range payload {
		if err := validatePath(k); err != nil {
			return nil, err
		}

		cleanPayload[filepath.Clean(k)] = content
	}

	return cleanPayload, nil
}

// validatePath validates a single path, returning an error if the path is
// invalid.  paths may not:
//
// 1. be absolute
// 2. contain '..' as an element
// 3. start with '..'
// 4. contain filenames larger than 255 characters
// 5. be longer than 4096 characters
func validatePath(targetPath string) error {
	// TODO: somehow unify this with the similar api validation,
	// validateVolumeSourcePath; the error semantics are just different enough
	// from this that it was time-prohibitive trying to find the right
	// refactoring to re-use.
	if targetPath == "" {
		return fmt.Errorf("invalid path: must not be empty: %q", targetPath)
	}
	if path.IsAbs(targetPath) {
		return fmt.Errorf("invalid path: must be relative path: %s", targetPath)
	}

	if len(targetPath) > maxPathLength {
		return fmt.Errorf("invalid path: must be less than or equal to %d characters", maxPathLength)
	}

	items := strings.Split(targetPath, string(os.PathSeparator))
	for _, item := range items {
		if item == ".." {
			return fmt.Errorf("invalid path: must not contain '..': %s", targetPath)
		}
		if len(item) > maxFileNameLength {
			return fmt.Errorf("invalid path: filenames must be less than or equal to %d characters", maxFileNameLength)
		}
	}
	if strings.HasPrefix(items[0], "..") && len(items[0]) > 2 {
		return fmt.Errorf("invalid path: must not start with '..': %s", targetPath)
	}

	return nil
}

// shouldWritePayload returns whether the payload should be written to disk.
func shouldWritePayload(payload map[string]FileProjection, oldTsDir string) (bool, error) {
	for userVisiblePath, fileProjection := range payload {
		shouldWrite, err := shouldWriteFile(path.Join(oldTsDir, userVisiblePath), fileProjection.Data)
		if err != nil {
			return false, err
		}

		if shouldWrite {
			return true, nil
		}
	}

	return false, nil
}

// shouldWriteFile returns whether a new version of a file should be written to disk.
func shouldWriteFile(path string, content []byte) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return true, nil
	}

	contentOnFs, err := ioutil.ReadFile(path)
	if err != nil {
		return false, err
	}

	return !bytes.Equal(content, contentOnFs), nil
}

// pathsToRemove walks the current version of the data directory and
// determines which paths should be removed (if any) after the payload is
// written to the target directory.
func (w *Writer) pathsToRemove(payload map[string]FileProjection, oldTsDir string) (sets.String, error) {
	paths := sets.NewString()
	visitor := func(path string, info os.FileInfo, err error) error {
		relativePath := strings.TrimPrefix(path, oldTsDir)
		relativePath = strings.TrimPrefix(relativePath, string(os.PathSeparator))
		if relativePath == "" {
			return nil
		}

		paths.Insert(relativePath)
		return nil
	}

	err := filepath.Walk(oldTsDir, visitor)
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	newPaths := sets.NewString()
	for file := range payload {
		// add all subpaths for the payload to the set of new paths
		// to avoid attempting to remove non-empty dirs
		for subPath := file; subPath != ""; {
			newPaths.Insert(subPath)
			subPath, _ = filepath.Split(subPath)
			subPath = strings.TrimSuffix(subPath, string(os.PathSeparator))
		}
	}

	result := paths.Difference(newPaths)
	if len(result) > 0 {
		log.Infof(context.Background(), "paths %v to remove in target directory %s", result.List(), w.targetDir)
	}

	return result, nil
}

// newTimestampDir creates a new timestamp directory
func (w *Writer) newTimestampDir() (string, error) {
	tsDir, err := ioutil.TempDir(w.targetDir, time.Now().UTC().Format("..2006_01_02_15_04_05."))
	if err != nil {
		log.Errorf(context.Background(), "create new temp directory failed: %s", err)
		return "", err
	}

	// 0755 permissions are needed to allow 'group' and 'other' to recurse the
	// directory tree.  do a chmod here to ensure that permissions are set correctly
	// regardless of the process' umask.
	err = os.Chmod(tsDir, 0755)
	if err != nil {
		log.Errorf(context.Background(), "set mode on new temp directory failed: %s", err)
		return "", err
	}

	return tsDir, nil
}

// writePayloadToDir writes the given payload to the given directory.  The
// directory must exist.
func (w *Writer) writePayloadToDir(payload map[string]FileProjection, dir string) error {
	for userVisiblePath, fileProjection := range payload {
		content := fileProjection.Data
		mode := os.FileMode(fileProjection.Mode)
		fullPath := path.Join(dir, userVisiblePath)
		baseDir, _ := filepath.Split(fullPath)

		err := os.MkdirAll(baseDir, os.ModePerm)
		if err != nil {
			log.Errorf(context.Background(), "create directory %s failed: %s", baseDir, err)
			return err
		}

		err = ioutil.WriteFile(fullPath, content, mode)
		if err != nil {
			log.Errorf(context.Background(), "write file %s mode %v failed: %s", fullPath, mode, err)
			return err
		}
		// Chmod is needed because ioutil.WriteFile() ends up calling
		// open(2) to create the file, so the final mode used is "mode &
		// ~umask". But we want to make sure the specified mode is used
		// in the file no matter what the umask is.
		err = os.Chmod(fullPath, mode)
		if err != nil {
			log.Errorf(context.Background(), "write file %s mode %v failed", fullPath, mode, err)
		}
	}

	return nil
}

// createUserVisibleFiles creates the relative symlinks for all the
// files configured in the payload. If the directory in a file path does not
// exist, it is created.
//
// Viz:
// For files: "bar", "foo/bar", "baz/bar", "foo/baz/blah"
// the following symlinks are created:
// bar -> ..data/bar
// foo -> ..data/foo
// baz -> ..data/baz
func (w *Writer) createUserVisibleFiles(payload map[string]FileProjection) error {
	for userVisiblePath := range payload {
		slashpos := strings.Index(userVisiblePath, string(os.PathSeparator))
		if slashpos == -1 {
			slashpos = len(userVisiblePath)
		}
		linkname := userVisiblePath[:slashpos]
		_, err := os.Readlink(path.Join(w.targetDir, linkname))
		if err != nil && os.IsNotExist(err) {
			// The link into the data directory for this path doesn't exist; create it
			visibleFile := path.Join(w.targetDir, linkname)
			dataDirFile := path.Join(dataDirName, linkname)

			err = os.Symlink(dataDirFile, visibleFile)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// removeUserVisiblePaths removes the set of paths from the user-visible
// portion of the writer's target directory.
func (w *Writer) removeUserVisiblePaths(paths sets.String) error {
	ps := string(os.PathSeparator)
	var lasterr error
	for p := range paths {
		// only remove symlinks from the volume root directory (i.e. items that don't contain '/')
		if strings.Contains(p, ps) {
			continue
		}
		if err := os.Remove(path.Join(w.targetDir, p)); err != nil {
			log.Errorf(context.Background(), "prune old user-visible path %s failed: %s", p, err)
			lasterr = err
		}
	}

	return lasterr
}
