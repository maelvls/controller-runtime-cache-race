# The metadata vs. concrete controller-runtime clients race

The controller-runtime is a Go client for Kubernetes that re-uses some
parts of client-go (the official Go client for Kubernetes). Unlike
client-go, controller-runtime knows how to create "lightweight"
caches.

We discovered in [controller-runtime#1660](https://github.com/kubernetes-sigs/controller-runtime/issues/1660)
that users of controller-runtime may unknowningly create multiple
"projections" (i.e., multiple ways of caching a resource) due to
the fact that controller-runtime creates caches "on the fly" the
first time you do a Get or List call.

To reproduce the "race" between two projections (metadata and concrete), I
created the following `main_test.go` that watches and reconciles Secrets. The
two most important features of this controller:

- the controller watches Secrets using the "metadata projection",
- the controller does a `client.Get` on the concrete Secret resource.

This simple Secret controller does a single thing: it adds the `"secret-found":
"yes"` annotation to the Secret if it does not already have it:


```
+-----------------------+
| kind: Secret          |
| metadata:             |
|   name: example-1     |
|   annotations: []     |
+-----------------------+
            |
            |
            | reconciliation
            |
            v
+-------------------------+
| kind: Secret            |
| metadata:               |
|   name: example-1       |
|   annotations:          |
|     secret-found: "yes" |
+-------------------------+
```

In order to debug the reason for this race, I add a log line in client-go's
reflector (that's why I "vendored" the dependencies using `go mod vendor`).

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

When a race happens, you can see that the event `ADDED` is processed at
different times. In the below example, the first `ADDED` is what triggers the
reconciliation of the Secret. The second `ADDED` is the one that supposedly
should be updating the cache that `client.Get` hits. But since the second
`ADDED` event is processed after the end of the reconciliation, the Secret is
not found.

```
    main_test.go:153: Create Secret secret-1 in namespace ns-1 owned
    main_test.go:197: : POST https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets 201 Created in 2 milliseconds
    main_test.go:197: : reflector: event ADDED (1 -> 159)
    main_test.go:197: secret-reconciler: start: secret="ns-1/secret-1"
	main_test.go:197: secret-reconciler: secret not found: secret="ns-1/secret-1"
    main_test.go:197: secret-reconciler: end: secret="ns-1/secret-1"
    main_test.go:197: : reflector: event ADDED (1 -> 159)
```

When the race does not manifests itself, the two `ADDED` events are processed
simultaneously:

```
    main_test.go:153: Create Secret secret-1 in namespace ns-1 owned
    main_test.go:197: : POST https://127.0.0.1:37589/api/v1/namespaces/ns-1/secrets 201 Created in 3 milliseconds
    main_test.go:197: : reflector: event ADDED (1 -> 159)
    main_test.go:197: : reflector: event ADDED (1 -> 159)
    main_test.go:197: secret-reconciler: start: secret="ns-1/secret-1"
    main_test.go:197: : GET https://127.0.0.1:37589/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 0 milliseconds
    main_test.go:197: : reflector: event MODIFIED (159 -> 160)
    main_test.go:197: : PUT https://127.0.0.1:37589/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 1 milliseconds
    main_test.go:197: secret-reconciler: end: secret="ns-1/secret-1"
```

## Logs of a failed test

```
--- FAIL: Test_secretController (20.17s)
    main_test.go:197: test-env: starting control plane
    main_test.go:197: test-env: installing CRDs
    main_test.go:197: test-env: installing webhooks
    main_test.go:197: metrics: metrics server is starting to listen: addr=":0"
    main_test.go:133: Force creation of the v1.Secret informer
    main_test.go:197: : Starting reflector *v1.Secret (9h5m45.258660894s) from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:151
    main_test.go:197: : Listing and watching *v1.Secret from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:151
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/secrets?limit=500&resourceVersion=0 200 OK in 0 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/secrets?allowWatchBookmarks=true&resourceVersion=1&timeoutSeconds=438&watch=true 200 OK in 0 milliseconds
    main_test.go:197: : caches populated
    main_test.go:197: : starting metrics server: path="/metrics"
    main_test.go:197: secret: Starting EventSource: reconciler group="" reconciler kind="Secret" source="kind source: /v1, Kind=Secret"
    main_test.go:197: secret: Starting Controller: reconciler group="" reconciler kind="Secret"
    main_test.go:197: : Starting reflector *v1.PartialObjectMetadata (10h33m8.188257002s) from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:255
    main_test.go:197: : Listing and watching *v1.PartialObjectMetadata from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:255
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/secrets?limit=500&resourceVersion=0 200 OK in 0 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/secrets?allowWatchBookmarks=true&resourceVersion=1&timeout=8m6s&timeoutSeconds=486&watch=true 200 OK in 0 milliseconds
    main_test.go:197: : caches populated
    main_test.go:197: : caches populated
    main_test.go:197: secret: Starting workers: reconciler group="" reconciler kind="Secret" worker count="1"
    main_test.go:197: : POST https://127.0.0.1:37809/api/v1/namespaces 201 Created in 1 milliseconds
    main_test.go:153: Create Secret secret-1 in namespace ns-1 owned
    main_test.go:197: : POST https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets 201 Created in 2 milliseconds
    main_test.go:197: : reflector: event ADDED (1 -> 159)
    main_test.go:197: secret-reconciler: start: secret="ns-1/secret-1"
	main_test.go:197: secret-reconciler: secret not found: secret="ns-1/secret-1"
    main_test.go:197: secret-reconciler: end: secret="ns-1/secret-1"
    main_test.go:197: : reflector: event ADDED (1 -> 159)
    main_test.go:162: Waiting for Secret to have the annotation secret-found=yes
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 1 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 2 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 3 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 2 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 2 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 4 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 1 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 1 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 2 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37809/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 2 milliseconds
    main_test.go:197: secret: Shutdown signal received, waiting for all workers to finish: reconciler group="" reconciler kind="Secret"
    main_test.go:197: secret: All workers finished: reconciler group="" reconciler kind="Secret"
    main_test.go:197: : Stopping reflector *v1.PartialObjectMetadata (10h33m8.188257002s) from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:255
    main_test.go:197: : Stopping reflector *v1.Secret (9h5m45.258660894s) from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:151
    main_test.go:176
                Error Trace:    main_test.go:176
                Error:          Received unexpected error:
                                timed out waiting for the condition
                Test:           Test_secretController
FAIL
```

## Logs of a successful run

```
go test . -v -count=1
=== RUN   Test_secretController
    main_test.go:197: test-env: starting control plane
    main_test.go:197: test-env: installing CRDs
    main_test.go:197: test-env: installing webhooks
    main_test.go:197: metrics: metrics server is starting to listen: addr=":0"
    main_test.go:133: Force creation of the v1.Secret informer
    main_test.go:197: : Starting reflector *v1.Secret (10h19m59.526981176s) from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:151
    main_test.go:197: : Listing and watching *v1.Secret from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:151
    main_test.go:197: : GET https://127.0.0.1:37589/api/v1/secrets?limit=500&resourceVersion=0 200 OK in 0 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37589/api/v1/secrets?allowWatchBookmarks=true&resourceVersion=1&timeoutSeconds=490&watch=true 200 OK in 0 milliseconds
    main_test.go:197: : caches populated
    main_test.go:197: : starting metrics server: path="/metrics"
    main_test.go:197: secret: Starting EventSource: reconciler group="" reconciler kind="Secret" source="kind source: /v1, Kind=Secret"
    main_test.go:197: secret: Starting Controller: reconciler group="" reconciler kind="Secret"
    main_test.go:197: : Starting reflector *v1.PartialObjectMetadata (9h48m22.729586202s) from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:255
    main_test.go:197: : Listing and watching *v1.PartialObjectMetadata from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:255
    main_test.go:197: : GET https://127.0.0.1:37589/api/v1/secrets?limit=500&resourceVersion=0 200 OK in 0 milliseconds
    main_test.go:197: : GET https://127.0.0.1:37589/api/v1/secrets?allowWatchBookmarks=true&resourceVersion=1&timeout=5m5s&timeoutSeconds=305&watch=true 200 OK in 0 milliseconds
    main_test.go:197: : caches populated
    main_test.go:197: : caches populated
    main_test.go:197: secret: Starting workers: reconciler group="" reconciler kind="Secret" worker count="1"
    main_test.go:197: : POST https://127.0.0.1:37589/api/v1/namespaces 201 Created in 2 milliseconds
    main_test.go:153: Create Secret secret-1 in namespace ns-1 owned
    main_test.go:197: : POST https://127.0.0.1:37589/api/v1/namespaces/ns-1/secrets 201 Created in 3 milliseconds
    main_test.go:197: : reflector: event ADDED (1 -> 159)
    main_test.go:197: : reflector: event ADDED (1 -> 159)
    main_test.go:162: Waiting for Secret to have the annotation secret-found=yes
    main_test.go:197: secret-reconciler: start: secret="ns-1/secret-1"
    main_test.go:197: : GET https://127.0.0.1:37589/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 0 milliseconds
    main_test.go:197: : reflector: event MODIFIED (159 -> 160)
    main_test.go:197: : PUT https://127.0.0.1:37589/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 1 milliseconds
    main_test.go:197: secret-reconciler: end: secret="ns-1/secret-1"
    main_test.go:197: : reflector: event MODIFIED (159 -> 160)
    main_test.go:197: secret-reconciler: start: secret="ns-1/secret-1"
    main_test.go:197: secret-reconciler: end: secret="ns-1/secret-1"
    main_test.go:197: : GET https://127.0.0.1:37589/api/v1/namespaces/ns-1/secrets/secret-1 200 OK in 1 milliseconds
    main_test.go:197: secret: Shutdown signal received, waiting for all workers to finish: reconciler group="" reconciler kind="Secret"
    main_test.go:197: secret: All workers finished: reconciler group="" reconciler kind="Secret"
    main_test.go:197: : Stopping reflector *v1.PartialObjectMetadata (9h48m22.729586202s) from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:255
    main_test.go:197: : Stopping reflector *v1.Secret (10h19m59.526981176s) from sigs.k8s.io/controller-runtime/pkg/cache/internal/informers_map.go:151
--- PASS: Test_secretController (8.00s)
PASS
ok      controller-runtime-cache-race   8.012s
```
