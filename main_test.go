package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// In this test, we create a tiny Secret controller that does one single thing:
// it adds the secret-found=yes annotation if it does not already have it.
//
//  +-----------------------+
//  | kind: Secret          |
//  | metadata:             |
//  |   name: example-1     |
//  |   annotations: []     |
//  +-----------------------+
//              |
//              |
//              | reconciliation = add annotation
//              |
//              v
//  +-------------------------+
//  | kind: Secret            |
//  | metadata:               |
//  |   name: example-1       |
//  |   annotations:          |
//  |     secret-found: "yes" |
//  +-------------------------+
//
func setupConfigMapReconciler(mgr manager.Manager, log logr.Logger) error {

	log = log.WithName("secret-reconciler")

	err := ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.OnlyMetadata).
		Complete(reconcile.Func(func(_ context.Context, r reconcile.Request) (reconcile.Result, error) {
			log := log
			log = log.WithValues("secret", r.NamespacedName)
			log.Info("start")
			defer log.Info("end")

			secret := &corev1.Secret{}
			err := mgr.GetClient().Get(context.Background(), r.NamespacedName, secret)
			switch {
			// If the secret doesn't exist, the reconciliation is done.
			case apierrors.IsNotFound(err):
				log.Info("secret not found")
				return reconcile.Result{}, nil
			case err != nil:
				return reconcile.Result{}, fmt.Errorf("looking for Secret %s: %w", r.NamespacedName, err)
			}

			if secret.Annotations != nil && secret.Annotations["secret-found"] == "yes" {
				return reconcile.Result{}, nil
			}

			if secret.Annotations == nil {
				secret.Annotations = make(map[string]string)
			}
			secret.Annotations["secret-found"] = "yes"
			err = mgr.GetClient().Update(context.Background(), secret)
			if err != nil {
				return reconcile.Result{}, err
			}

			return reconcile.Result{}, nil
		}))
	if err != nil {
		return fmt.Errorf("while completing new controller: %w", err)
	}

	return nil
}

func Test_secretController(t *testing.T) {
	logger := TestLogger{T: t}
	ctrl.SetLogger(logger)
	klog.SetLogger(logger)
	klogFlags := flag.NewFlagSet("klog", flag.ExitOnError)
	klog.InitFlags(klogFlags)
	_ = klogFlags.Set("v", "6")

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	testEnv := &envtest.Environment{Scheme: scheme}
	rc, err := testEnv.Start()
	require.NoError(t, err)
	defer func() {
		require.NoError(t, testEnv.Stop())
	}()

	const timeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.TODO(), timeout)
	defer cancel()

	kc, err := client.New(rc, client.Options{Scheme: scheme})
	require.NoError(t, err)

	mgr, err := ctrl.NewManager(rc, ctrl.Options{
		Scheme:             scheme,
		Logger:             logger,
		MetricsBindAddress: ":0", // Avoids :8080 clashes when run with GNU parallel.
	})
	require.NoError(t, err)

	err = setupConfigMapReconciler(mgr, logger)
	require.NoError(t, err)

	go func() {
		require.NoError(t, mgr.Start(ctx))
	}()

	t.Log("Force creation of the v1.Secret informer")
	// Dummy call that forces the reflector and informer to be created/started,
	// since we want the reflector to already be running when the ADDED event
	// comes in. If we didn't do this, the "race" between the two reflectors
	// (which are updating the two Secret caches) would not happen since the
	// first client.Get call, which creates the reflector/watch/informer does
	// not hit the cache (or rather, it does, but at this point the cache is up
	// to date).
	mgr.GetClient().Get(context.Background(), types.NamespacedName{}, &corev1.Secret{})
	time.Sleep(300 * time.Millisecond)

	const nsName = "ns-1"
	ns1 := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: nsName,
		},
	}
	require.NoError(t, kc.Create(ctx, &ns1))

	name := "secret-1"
	t.Logf("Create Secret %s in namespace %s owned", name, nsName)
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: nsName,
		},
	}
	require.NoError(t, kc.Create(ctx, &secret))

	t.Log("Waiting for Secret to have the annotation secret-found=yes")
	err = pollUntil(ctx, time.Second, timeout, func() (done bool, err error) {
		var secret corev1.Secret
		err = kc.Get(ctx, types.NamespacedName{Name: name, Namespace: ns1.Name}, &secret)
		if err != nil {
			return false, err
		}

		if secret.Annotations["secret-found"] != "yes" {
			return false, nil
		}

		return true, nil
	})
	require.NoError(t, err)
}

func pollUntil(ctx context.Context, interval time.Duration, timeout time.Duration, f wait.ConditionFunc) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return wait.PollImmediateUntil(interval, f, ctx.Done())
}

// TestLogger is a logr.Logger that prints everything to t.Log.
type TestLogger struct {
	T          *testing.T
	name       string
	withValues []string
}

func (log TestLogger) Info(msg string, keysAndValues ...interface{}) {
	withValues := append([]string{}, log.withValues...)
	for i := 0; i < len(keysAndValues); i = i + 2 {
		withValues = append(withValues, fmt.Sprintf(`%s="%v"`, keysAndValues[i], keysAndValues[i+1]))
	}
	log.T.Logf("%s: %s: %v", log.name, strings.TrimSpace(msg), strings.Join(withValues, " "))
}

func (log TestLogger) Error(err error, msg string, args ...interface{}) {
	log.T.Logf("%s: %s: %v: %v", log.name, strings.TrimSpace(msg), err, args)
}

func (TestLogger) Enabled() bool {
	return true
}

func (log TestLogger) V(v int) logr.Logger {
	return log
}

func (log TestLogger) WithName(name string) logr.Logger {
	log.name = name
	return log
}

func (log TestLogger) WithValues(keysAndValues ...interface{}) logr.Logger {
	for i := 0; i < len(keysAndValues); i = i + 2 {
		log.withValues = append(log.withValues, fmt.Sprintf(`%s="%v"`, keysAndValues[i], keysAndValues[i+1]))
	}
	return log
}
