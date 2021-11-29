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

package webhookmanager

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/ppltools/webhookmanager/config"
	"github.com/ppltools/webhookmanager/controller"
	"github.com/ppltools/webhookmanager/health"
)

func NewWebhookManager(ctx context.Context, handlers map[string]admission.Handler, opts ...config.SetOption) error {
	cfg := config.NewDefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	if err := health.InitDefaultChecker(
		health.SetListenPort(cfg.GetWebhookPort()),
		health.SetCaCertDir(cfg.GetCertDir()),
	); err != nil {
		return err
	}

	ctrl, err := controller.New(ctx, cfg, handlers)
	if err != nil {
		return err
	}
	go func() {
		ctrl.Start(ctx)
	}()

	timer := time.NewTimer(cfg.GetInitTimeout())
	defer timer.Stop()
	select {
	case <-controller.Inited():
		return nil
	case <-timer.C:
		return fmt.Errorf("failed to start webhook controller for waiting more than %v", cfg.GetInitTimeout())
	}
}
