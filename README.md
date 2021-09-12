### https://github.com/jetstack/venafi-oauth-helper/pull/67

To reproduce the "race" between two projections (metadata and concrete), I
created the following `main_test.go` that watches and reconciles Secrets. The
two most important features of this controller:

- the controller watches Secrets using the "metadata projection",
- the controller does a `client.Get` on the concrete Secret resource.

In order to debug the reason for this race, I add a log line in client-go's
reflector (that's why I vendored the dependencies).

```diff
diff --git a/vendor/k8s.io/client-go/tools/cache/reflector.go b/vendor/k8s.io/client-go/tools/cache/reflector.go
index 90fa45d..f6240b6 100644
--- a/vendor/k8s.io/client-go/tools/cache/reflector.go
+++ b/vendor/k8s.io/client-go/tools/cache/reflector.go
@@ -495,6 +495,9 @@ loop:
 				continue
 			}
 			newResourceVersion := meta.GetResourceVersion()
+
+			klog.V(4).Infof("reflector: event %s (%s -> %s)", event.Type, *resourceVersion, newResourceVersion)
+
 			switch event.Type {
 			case watch.Added:
 				err := r.store.Add(event.Object)
```

The above patch is already applied to this repository.

To run the test, install GNU parallel (`brew install parallel`, `apt install parallel`) and run:

```sh
curl -sSLo envtest-bins.tar.gz https://storage.googleapis.com/kubebuilder-tools/kubebuilder-tools-1.19.2-linux-amd64.tar.gz
export TEST_ASSET_ETCD=/opt/bin/etcd
export TEST_ASSET_KUBE_APISERVER=/opt/bin/kube-apiserver
export TEST_ASSET_KUBECTL=/opt/bin/kubectl

# GNU parallel (`sudo apt install parallel` or `sudo dnf install parallel`)
parallel "go test ./main_test.go -count=1" ::: {1..100}
```
