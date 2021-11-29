/*
Copyright 2021 The Stupig Authors.

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

package log

import (
	"context"

	"k8s.io/klog/v2"
)

type Log interface {
	Infof(ctx context.Context, format string, args ...interface{})
	Errorf(ctx context.Context, format string, args ...interface{})
}

var logger Log

func SetLogger(log Log) {
	logger = log
}

func Infof(ctx context.Context, format string, args ...interface{}) {
	if logger == nil {
		klog.Infof(format, args...)
	} else {
		logger.Infof(ctx, format, args...)
	}
}

func Errorf(ctx context.Context, format string, args ...interface{}) {
	if logger == nil {
		klog.Errorf(format, args...)
	} else {
		logger.Errorf(ctx, format, args...)
	}
}
