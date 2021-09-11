### https://github.com/jetstack/venafi-oauth-helper/pull/67

To reproduce the "race" between two projections (metadata and concrete), I
created the following `main_test.go` that watches and reconciles Secrets. The
two most important features of this controller:

- the controller watches Secrets using the "metadata projection",
- the controller does a `client.Get` on the concrete Secret resource.

In order to debug the reason for this race, I add a log line in client-go's
reflector:

```diff
+klog.V(4).Infof("reflector: event %s (%s -> %s)", event.Type, *resourceVersion, newResourceVersion)
switch event.Type {
case watch.Added:
	err := r.store.Add(event.Object)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("%s: unable to add watch event object (%#v) to store: %v", r.name, event.Object, err))
	}
case watch.Modified:
	err := r.store.Update(event.Object)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("%s: unable to update watch event object (%#v) to store: %v", r.name, event.Object, err))
	}
```

```sh
# GNU parallel (`sudo apt install parallel` or `sudo dnf install parallel`)
parallel "go test ./main_test.go -count=1" ::: {1..100}
```

