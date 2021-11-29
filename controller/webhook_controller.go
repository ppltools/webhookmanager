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

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	admissionregistrationinformers "k8s.io/client-go/informers/admissionregistration/v1beta1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/ppltools/webhookmanager/config"
	"github.com/ppltools/webhookmanager/configuration"
	"github.com/ppltools/webhookmanager/generator"
	"github.com/ppltools/webhookmanager/log"
	"github.com/ppltools/webhookmanager/writer"
)

const (
	defaultResyncPeriod = time.Minute
)

var (
	uninit   = make(chan struct{})
	onceInit = sync.Once{}
)

func Inited() chan struct{} {
	return uninit
}

type Controller struct {
	*config.Config

	kubeClient clientset.Interface
	handlers   map[string]admission.Handler

	informerFactory informers.SharedInformerFactory
	synced          []cache.InformerSynced

	queue workqueue.RateLimitingInterface
}

func New(ctx context.Context, cfg *config.Config, handlers map[string]admission.Handler) (*Controller, error) {
	c := &Controller{
		Config:     cfg,
		kubeClient: clientset.NewForConfigOrDie(cfg.GetKubeConfig()),
		handlers:   handlers,
		queue:      workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "webhook-controller"),
	}

	c.informerFactory = informers.NewSharedInformerFactory(c.kubeClient, 0)

	secretInformer := coreinformers.New(c.informerFactory, c.GetNamespace(), nil).Secrets()
	admissionRegistrationInformer := admissionregistrationinformers.New(c.informerFactory, v1.NamespaceAll, nil)

	secretInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			secret := obj.(*v1.Secret)
			if secret.Name == c.GetSecretName() {
				log.Infof(ctx, "Secret %s added", c.GetSecretName())
				c.queue.Add("")
			}
		},
		UpdateFunc: func(old, cur interface{}) {
			secret := cur.(*v1.Secret)
			if secret.Name == c.GetSecretName() {
				log.Infof(ctx, "Secret %s updated", c.GetSecretName())
				c.queue.Add("")
			}
		},
	})

	admissionRegistrationInformer.MutatingWebhookConfigurations().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			conf := obj.(*admissionregistrationv1beta1.MutatingWebhookConfiguration)
			if conf.Name == c.GetMutatingWebhookName() {
				log.Infof(ctx, "MutatingWebhookConfiguration %s added", c.GetMutatingWebhookName())
				c.queue.Add("")
			}
		},
		UpdateFunc: func(old, cur interface{}) {
			conf := cur.(*admissionregistrationv1beta1.MutatingWebhookConfiguration)
			if conf.Name == c.GetMutatingWebhookName() {
				log.Infof(ctx, "MutatingWebhookConfiguration %s update", c.GetMutatingWebhookName())
				c.queue.Add("")
			}
		},
	})

	admissionRegistrationInformer.ValidatingWebhookConfigurations().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			conf := obj.(*admissionregistrationv1beta1.ValidatingWebhookConfiguration)
			if conf.Name == c.GetValidatingWebhookName() {
				log.Infof(ctx, "ValidatingWebhookConfiguration %s added", c.GetValidatingWebhookName())
				c.queue.Add("")
			}
		},
		UpdateFunc: func(old, cur interface{}) {
			conf := cur.(*admissionregistrationv1beta1.ValidatingWebhookConfiguration)
			if conf.Name == c.GetValidatingWebhookName() {
				log.Infof(ctx, "ValidatingWebhookConfiguration %s updated", c.GetValidatingWebhookName())
				c.queue.Add("")
			}
		},
	})

	c.synced = []cache.InformerSynced{
		secretInformer.Informer().HasSynced,
		admissionRegistrationInformer.MutatingWebhookConfigurations().Informer().HasSynced,
		admissionRegistrationInformer.ValidatingWebhookConfigurations().Informer().HasSynced,
	}

	return c, nil
}

func (c *Controller) Start(ctx context.Context) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	log.Infof(ctx, "Starting webhook-controller")
	defer log.Infof(ctx, "Shutting down webhook-controller")

	c.informerFactory.Start(ctx.Done())
	if !cache.WaitForNamedCacheSync("webhook-controller", ctx.Done(), c.synced...) {
		return
	}

	go wait.Until(func() {
		for c.processNextWorkItem() {
		}
	}, time.Second, ctx.Done())
	log.Infof(ctx, "Started webhook-controller")

	<-ctx.Done()
}

func (c *Controller) processNextWorkItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	err := c.sync()
	if err == nil {
		c.queue.AddAfter(key, defaultResyncPeriod)
		c.queue.Forget(key)
		return true
	}

	log.Errorf(context.Background(), "sync %q failed with %v", key, err)
	c.queue.AddRateLimited(key)

	return true
}

func (c *Controller) sync() error {
	log.Infof(context.Background(), "Starting to sync webhook certs and configurations")
	defer func() {
		log.Infof(context.Background(), "Finished to sync webhook certs and configurations")
	}()

	var dnsName string
	var certWriter writer.CertWriter
	var err error

	if dnsName = c.GetWebhookHost(); len(dnsName) == 0 {
		dnsName = generator.ServiceToCommonName(c.GetNamespace(), c.GetServiceName())
	}
	certWriter, err = writer.NewSecretCertWriter(writer.SecretCertWriterOptions{
		Clientset: c.kubeClient,
		Secret:    &types.NamespacedName{Namespace: c.GetNamespace(), Name: c.GetSecretName()},
	})
	if err != nil {
		return fmt.Errorf("failed to ensure certs: %v", err)
	}

	certs, _, err := certWriter.EnsureCert(dnsName)
	if err != nil {
		return fmt.Errorf("failed to ensure certs: %v", err)
	}
	if err := writer.WriteCertsToDir(c.GetCertDir(), certs); err != nil {
		return fmt.Errorf("failed to write certs to dir: %v", err)
	}

	if err := configuration.Ensure(c.Config, c.kubeClient, c.handlers, certs.CACert); err != nil {
		return fmt.Errorf("failed to ensure configuration: %v", err)
	}

	onceInit.Do(func() {
		close(uninit)
	})
	return nil
}
