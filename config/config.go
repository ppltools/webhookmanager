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

package config

import (
	"fmt"
	"time"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"

	"github.com/ppltools/webhookmanager/log"
	"github.com/ppltools/webhookmanager/util"
)

type Config struct {
	*rest.Config
	InitTimeout time.Duration
	Service     ServiceConfig
	Cert        CertConfig
	Webhook     WebhookConfig
}

type ServiceConfig struct {
	Namespace   string
	ServiceName string
	CrdGroup    sets.String
}

type CertConfig struct {
	CertDir    string
	SecretName string
}

type WebhookConfig struct {
	Host           string
	Port           int
	MutatingName   string
	ValidatingName string
}

func (c *Config) GetKubeConfig() *rest.Config {
	return c.Config
}

func (c *Config) GetInitTimeout() time.Duration {
	return c.InitTimeout
}

func (c *Config) GetNamespace() string {
	return c.Service.Namespace
}

func (c *Config) GetServiceName() string {
	return c.Service.ServiceName
}

func (c *Config) GetCrdGroup() sets.String {
	return c.Service.CrdGroup
}

func (c *Config) GetCertDir() string {
	return c.Cert.CertDir
}

func (c *Config) GetSecretName() string {
	return c.Cert.SecretName
}

func (c *Config) GetWebhookHost() string {
	return c.Webhook.Host
}

func (c *Config) GetWebhookPort() int {
	return c.Webhook.Port
}

func (c *Config) GetMutatingWebhookName() string {
	return c.Webhook.MutatingName
}

func (c *Config) GetValidatingWebhookName() string {
	return c.Webhook.ValidatingName
}

func (c *Config) Validate() error {
	errs := make([]error, 0)
	if len(c.Service.Namespace) == 0 {
		errs = append(errs, fmt.Errorf("namespace cannot be empty"))
	}
	if len(c.Webhook.Host) > 0 && len(c.Service.ServiceName) > 0 {
		errs = append(errs, fmt.Errorf("webhook host and service name cannot be both specified"))
	}
	if len(c.Webhook.MutatingName) == 0 || len(c.Webhook.ValidatingName) == 0 {
		errs = append(errs, fmt.Errorf("webhook configuration name cannot be empty"))
	}
	return utilerrors.NewAggregate(errs)
}

func NewDefaultConfig() *Config {
	return &Config{
		InitTimeout: time.Second * 20,
		Service: ServiceConfig{
			Namespace:   util.GetNamespace(),
			ServiceName: util.GetServiceName(),
		},
		Cert: CertConfig{
			CertDir:    util.GetCertDir(),
			SecretName: util.GetSecretName(),
		},
		Webhook: WebhookConfig{
			Host: util.GetHost(),
			Port: util.GetPort(),
		},
	}
}

type SetOption func(c *Config)

func SetLogger(logger log.Log) SetOption {
	return func(c *Config) {
		log.SetLogger(logger)
	}
}

func SetKubeConfig(cfg *rest.Config) SetOption {
	return func(c *Config) {
		c.Config = cfg
	}
}

func SetInitTimeout(timeout time.Duration) SetOption {
	return func(c *Config) {
		c.InitTimeout = timeout
	}
}

func SetCrdGroups(groups []string) SetOption {
	return func(c *Config) {
		c.Service.CrdGroup = sets.NewString(groups...)
	}
}

func SetCertDir(certDir string) SetOption {
	return func(c *Config) {
		c.Cert.CertDir = certDir
	}
}

func SetSecretName(secretName string) SetOption {
	return func(c *Config) {
		c.Cert.SecretName = secretName
	}
}

func SetWehbookAddr(host string, port int) SetOption {
	return func(c *Config) {
		c.Webhook.Host = host
		c.Webhook.Port = port
	}
}

func SetWebhookName(namePrefix string) SetOption {
	return func(c *Config) {
		c.Webhook.MutatingName = fmt.Sprintf("%s-%s", namePrefix, "mutating-webhook-configuration")
		c.Webhook.ValidatingName = fmt.Sprintf("%s-%s", namePrefix, "validating-webhook-configuration")
	}
}
