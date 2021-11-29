/*
Copyright 2021 The Stupig Authors.
Copyright 2020 The Kruise Authors.

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

package health

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"path"
	"sync"

	"github.com/fsnotify/fsnotify"

	"github.com/ppltools/webhookmanager/log"
)

type Checker struct {
	CaCertPath string
	ListenPort int

	onceWatch sync.Once
	lock      sync.Mutex
	client    *http.Client
}

type SetOption func(c *Checker)

func SetCaCertDir(cacertDir string) SetOption {
	return func(c *Checker) {
		c.CaCertPath = path.Join(cacertDir, "ca-cert.pem")
	}
}

func SetListenPort(port int) SetOption {
	return func(c *Checker) {
		c.ListenPort = port
	}
}

var Default *Checker

func InitDefaultChecker(opts ...SetOption) error {
	Default = new(Checker)
	for _, opt := range opts {
		opt(Default)
	}
	return nil
}

func (c *Checker) loadHTTPClientWithCACert() error {
	caCert, err := ioutil.ReadFile(c.CaCertPath)
	if err != nil {
		return err
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)
	c.lock.Lock()
	defer c.lock.Unlock()
	c.client = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: caCertPool,
			},
		},
	}
	return nil
}

func (c *Checker) watchCACert(ctx context.Context, watcher *fsnotify.Watcher) {
	for {
		select {
		case event, ok := <-watcher.Events:
			// Channel is closed.
			if !ok {
				return
			}

			// Only care about events which may modify the contents of the file.
			if !(isWrite(event) || isRemove(event) || isCreate(event)) {
				continue
			}

			log.Infof(ctx, "Watched ca-cert %v %v", event.Name, event.Op)

			// If the file was removed, re-add the watch.
			if isRemove(event) {
				if err := watcher.Add(event.Name); err != nil {
					log.Errorf(ctx, "Failed to re-watch ca-cert %v: %v", event.Name, err)
				}
			}

			if err := c.loadHTTPClientWithCACert(); err != nil {
				log.Errorf(ctx, "Failed to reload ca-cert %v: %v", event.Name, err)
			}

		case err, ok := <-watcher.Errors:
			// Channel is closed.
			if !ok {
				return
			}
			log.Errorf(ctx, "Failed to watch ca-cert: %v", err)
		}
	}
}

func isWrite(event fsnotify.Event) bool {
	return event.Op&fsnotify.Write == fsnotify.Write
}

func isCreate(event fsnotify.Event) bool {
	return event.Op&fsnotify.Create == fsnotify.Create
}

func isRemove(event fsnotify.Event) bool {
	return event.Op&fsnotify.Remove == fsnotify.Remove
}

func Check(_ *http.Request) error {
	if Default == nil {
		return fmt.Errorf("checker has not been initialized")
	}
	Default.onceWatch.Do(func() {
		if err := Default.loadHTTPClientWithCACert(); err != nil {
			panic(fmt.Errorf("failed to load ca-cert for the first time: %v", err))
		}
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			panic(fmt.Errorf("failed to new ca-cert watcher: %v", err))
		}
		if err = watcher.Add(Default.CaCertPath); err != nil {
			panic(fmt.Errorf("failed to add %v into watcher: %v", Default.CaCertPath, err))
		}
		go Default.watchCACert(context.Background(), watcher)
	})

	url := fmt.Sprintf("https://localhost:%d/healthz", Default.ListenPort)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Add("Content-Type", "application/json")

	Default.lock.Lock()
	defer Default.lock.Unlock()
	_, err = Default.client.Do(req)
	if err != nil {
		return err
	}
	return nil
}
