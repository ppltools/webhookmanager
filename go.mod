module github.com/ppltools/webhookmanager

go 1.16

require (
	github.com/fsnotify/fsnotify v1.4.9
	k8s.io/api v0.20.9
	k8s.io/apiextensions-apiserver v0.20.9 // indirect
	k8s.io/apimachinery v0.20.9
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/klog/v2 v2.4.0
	sigs.k8s.io/controller-runtime v0.6.4
)

replace k8s.io/client-go => k8s.io/client-go v0.20.9
