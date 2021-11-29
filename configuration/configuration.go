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

package configuration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"

	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/ppltools/webhookmanager/config"
	"github.com/ppltools/webhookmanager/log"
)

func Ensure(c *config.Config, kubeClient clientset.Interface, handlers map[string]admission.Handler, caBundle []byte) error {
	mutatingConfig, err := kubeClient.AdmissionregistrationV1beta1().MutatingWebhookConfigurations().Get(context.TODO(), c.GetMutatingWebhookName(), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("MutatingWebhookConfiguration %s not found", c.GetMutatingWebhookName())
	}
	validatingConfig, err := kubeClient.AdmissionregistrationV1beta1().ValidatingWebhookConfigurations().Get(context.TODO(), c.GetValidatingWebhookName(), metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("ValidatingWebhookConfiguration %s not found", c.GetValidatingWebhookName())
	}
	oldMutatingConfig := mutatingConfig.DeepCopy()
	oldValidatingConfig := validatingConfig.DeepCopy()

	mutatingTemplate, err := parseMutatingTemplate(mutatingConfig)
	if err != nil {
		return err
	}
	validatingTemplate, err := parseValidatingTemplate(validatingConfig)
	if err != nil {
		return err
	}

	var mutatingWHs []admissionregistrationv1beta1.MutatingWebhook
	for i := range mutatingTemplate {
		wh := &mutatingTemplate[i]
		wh.ClientConfig.CABundle = caBundle
		path, err := getPath(&wh.ClientConfig)
		if err != nil {
			return err
		}
		if _, ok := handlers[path]; !ok {
			log.Infof(context.Background(), "Webhook configuration for %s is not in my charge", path)
			mutatingWHs = append(mutatingWHs, *wh)
			continue
		}
		if wh.ClientConfig.Service != nil {
			wh.ClientConfig.Service.Namespace = c.GetNamespace()
			wh.ClientConfig.Service.Name = c.GetServiceName()
		}
		if host := c.GetWebhookHost(); len(host) > 0 && wh.ClientConfig.Service != nil {
			convertClientConfig(&wh.ClientConfig, host, c.GetWebhookPort())
		}
		mutatingWHs = append(mutatingWHs, *wh)
	}
	mutatingConfig.Webhooks = mutatingWHs

	var validatingWHs []admissionregistrationv1beta1.ValidatingWebhook
	for i := range validatingTemplate {
		wh := &validatingTemplate[i]
		wh.ClientConfig.CABundle = caBundle
		path, err := getPath(&wh.ClientConfig)
		if err != nil {
			return err
		}
		if _, ok := handlers[path]; !ok {
			log.Infof(context.Background(), "Webhook configuration for %s is not in my charge", path)
			validatingWHs = append(validatingWHs, *wh)
			continue
		}
		if wh.ClientConfig.Service != nil {
			wh.ClientConfig.Service.Namespace = c.GetNamespace()
			wh.ClientConfig.Service.Name = c.GetServiceName()
		}
		if host := c.GetWebhookHost(); len(host) > 0 && wh.ClientConfig.Service != nil {
			convertClientConfig(&wh.ClientConfig, host, c.GetWebhookPort())
		}
		validatingWHs = append(validatingWHs, *wh)
	}
	validatingConfig.Webhooks = validatingWHs

	if !reflect.DeepEqual(mutatingConfig, oldMutatingConfig) {
		if _, err := kubeClient.AdmissionregistrationV1beta1().MutatingWebhookConfigurations().Update(context.TODO(), mutatingConfig, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update %s: %v", c.GetMutatingWebhookName(), err)
		}
	}

	if !reflect.DeepEqual(validatingConfig, oldValidatingConfig) {
		if _, err := kubeClient.AdmissionregistrationV1beta1().ValidatingWebhookConfigurations().Update(context.TODO(), validatingConfig, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update %s: %v", c.GetValidatingWebhookName(), err)
		}
	}

	return nil
}

func getPath(clientConfig *admissionregistrationv1beta1.WebhookClientConfig) (string, error) {
	if clientConfig.Service != nil {
		return *clientConfig.Service.Path, nil
	} else if clientConfig.URL != nil {
		u, err := url.Parse(*clientConfig.URL)
		if err != nil {
			return "", err
		}
		return u.Path, nil
	}
	return "", fmt.Errorf("invalid clientConfig: %+v", clientConfig)
}

func convertClientConfig(clientConfig *admissionregistrationv1beta1.WebhookClientConfig, host string, port int) {
	url := fmt.Sprintf("https://%s:%d%s", host, port, *clientConfig.Service.Path)
	clientConfig.URL = &url
	clientConfig.Service = nil
}

func parseMutatingTemplate(mutatingConfig *admissionregistrationv1beta1.MutatingWebhookConfiguration) ([]admissionregistrationv1beta1.MutatingWebhook, error) {
	if templateStr := mutatingConfig.Annotations["template"]; len(templateStr) > 0 {
		var mutatingWHs []admissionregistrationv1beta1.MutatingWebhook
		if err := json.Unmarshal([]byte(templateStr), &mutatingWHs); err != nil {
			return nil, err
		}
		return mutatingWHs, nil
	}

	templateBytes, err := json.Marshal(mutatingConfig.Webhooks)
	if err != nil {
		return nil, err
	}
	if mutatingConfig.Annotations == nil {
		mutatingConfig.Annotations = make(map[string]string, 1)
	}
	mutatingConfig.Annotations["template"] = string(templateBytes)
	return mutatingConfig.Webhooks, nil
}

func parseValidatingTemplate(validatingConfig *admissionregistrationv1beta1.ValidatingWebhookConfiguration) ([]admissionregistrationv1beta1.ValidatingWebhook, error) {
	if templateStr := validatingConfig.Annotations["template"]; len(templateStr) > 0 {
		var validatingWHs []admissionregistrationv1beta1.ValidatingWebhook
		if err := json.Unmarshal([]byte(templateStr), &validatingWHs); err != nil {
			return nil, err
		}
		return validatingWHs, nil
	}

	templateBytes, err := json.Marshal(validatingConfig.Webhooks)
	if err != nil {
		return nil, err
	}
	if validatingConfig.Annotations == nil {
		validatingConfig.Annotations = make(map[string]string, 1)
	}
	validatingConfig.Annotations["template"] = string(templateBytes)
	return validatingConfig.Webhooks, nil
}
