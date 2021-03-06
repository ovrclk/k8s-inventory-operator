package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/cskr/pubsub"
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/gorilla/mux"
	akashv1 "github.com/ovrclk/akash/pkg/apis/akash.network/v1"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	"github.com/ovrclk/k8s-inventory-operator/util/runner"

	"github.com/boz/go-lifecycle"
	akashclientset "github.com/ovrclk/akash/pkg/client/clientset/versioned"
	rookclientset "github.com/rook/rook/pkg/client/clientset/versioned"
)

const (
	FlagKubeConfig    = "kubeconfig"
	FlagKubeInCluster = "kube-incluster"
	FlagApiTimeout    = "api-timeout"
	FlagQueryTimeout  = "query-timeout"
)

type ContextKey string

const (
	CtxKeyKubeConfig     = ContextKey(FlagKubeConfig)
	CtxKeyKubeClientSet  = ContextKey("kube-clientset")
	CtxKeyRookClientSet  = ContextKey("rook-clientset")
	CtxKeyAkashClientSet = ContextKey("akash-clientset")
	CtxKeyPubSub         = ContextKey("pubsub")
	CtxKeyLifecycle      = ContextKey("lifecycle")
	CtxKeyErrGroup       = ContextKey("errgroup")
	CtxKeyStorage        = ContextKey("storage")
)

func LogFromCtx(ctx context.Context) logr.Logger {
	return logr.FromContext(ctx)
}

func KubeConfigFromCtx(ctx context.Context) *rest.Config {
	val := ctx.Value(CtxKeyKubeConfig)
	if val == nil {
		panic("context does not have kubeconfig set")
	}

	return val.(*rest.Config)
}

func KubeClientFromCtx(ctx context.Context) *kubernetes.Clientset {
	val := ctx.Value(CtxKeyKubeClientSet)
	if val == nil {
		panic("context does not have kube client set")
	}

	return val.(*kubernetes.Clientset)
}

func RookClientFromCtx(ctx context.Context) *rookclientset.Clientset {
	val := ctx.Value(CtxKeyRookClientSet)
	if val == nil {
		panic("context does not have rook client set")
	}

	return val.(*rookclientset.Clientset)
}

func AkashClientFromCtx(ctx context.Context) *akashclientset.Clientset {
	val := ctx.Value(CtxKeyAkashClientSet)
	if val == nil {
		panic("context does not have akash client set")
	}

	return val.(*akashclientset.Clientset)
}

func PubSubFromCtx(ctx context.Context) *pubsub.PubSub {
	val := ctx.Value(CtxKeyPubSub)
	if val == nil {
		panic("context does not have pubsub set")
	}

	return val.(*pubsub.PubSub)
}

func LifecycleFromCtx(ctx context.Context) lifecycle.Lifecycle {
	val := ctx.Value(CtxKeyLifecycle)
	if val == nil {
		panic("context does not have lifecycle set")
	}

	return val.(lifecycle.Lifecycle)
}

func ErrGroupFromCtx(ctx context.Context) *errgroup.Group {
	val := ctx.Value(CtxKeyErrGroup)
	if val == nil {
		panic("context does not have errgroup set")
	}

	return val.(*errgroup.Group)
}

func StorageFromCtx(ctx context.Context) []Storage {
	val := ctx.Value(CtxKeyStorage)
	if val == nil {
		panic("context does not have storage set")
	}

	return val.([]Storage)
}

func ContextSet(c *cli.Context, key, val interface{}) {
	c.Context = context.WithValue(c.Context, key, val)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	zconf := zap.NewDevelopmentConfig()
	zconf.DisableCaller = true
	zconf.EncoderConfig.EncodeTime = func(time.Time, zapcore.PrimitiveArrayEncoder) {}

	zapLog, _ := zconf.Build()

	ctx = logr.NewContext(ctx, zapr.NewLogger(zapLog))

	app := cli.NewApp()
	app.ErrWriter = os.Stdout
	app.EnableBashCompletion = true
	app.ExitErrHandler = func(c *cli.Context, err error) {
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Println(err.Error())
			os.Exit(1)
		}
	}

	app.Flags = []cli.Flag{
		&cli.PathFlag{
			Name:    FlagKubeConfig,
			Usage:   "Load kube configuration from `FILE`",
			EnvVars: []string{strings.ToUpper(FlagKubeConfig)},
			Value:   path.Join(homedir.HomeDir(), ".kube", "config"),
		},
		&cli.BoolFlag{
			Name: FlagKubeInCluster,
		},
		&cli.DurationFlag{
			Name:     FlagApiTimeout,
			EnvVars:  []string{strings.ToUpper(FlagApiTimeout)},
			Required: false,
			Hidden:   false,
			Value:    3 * time.Second,
		},
		&cli.DurationFlag{
			Name:     FlagQueryTimeout,
			EnvVars:  []string{strings.ToUpper(FlagQueryTimeout)},
			Required: false,
			Hidden:   false,
			Value:    2 * time.Second,
		},
	}

	app.Before = func(c *cli.Context) error {
		err := loadKubeConfig(c)
		if err != nil {
			return err
		}

		kubecfg := KubeConfigFromCtx(c.Context)

		clientset, err := kubernetes.NewForConfig(kubecfg)
		if err != nil {
			return err
		}

		rc, err := rookclientset.NewForConfig(kubecfg)
		if err != nil {
			return err
		}

		ac, err := akashclientset.NewForConfig(kubecfg)

		group, ctx := errgroup.WithContext(c.Context)
		c.Context = ctx

		ContextSet(c, CtxKeyKubeClientSet, clientset)
		ContextSet(c, CtxKeyRookClientSet, rc)
		ContextSet(c, CtxKeyAkashClientSet, ac)
		ContextSet(c, CtxKeyPubSub, pubsub.New(1000))
		ContextSet(c, CtxKeyErrGroup, group)

		return nil
	}

	app.Action = func(c *cli.Context) error {
		bus := PubSubFromCtx(c.Context)
		kc := KubeClientFromCtx(c.Context)
		group := ErrGroupFromCtx(c.Context)

		var storage []Storage
		st, err := NewCeph(c.Context)
		if err != nil {
			return err
		}
		storage = append(storage, st)

		if st, err = NewRancher(c.Context); err != nil {
			return err
		}
		storage = append(storage, st)

		ContextSet(c, CtxKeyStorage, storage)

		srv := &http.Server{
			Addr:    ":8080",
			Handler: newRouter(LogFromCtx(c.Context).WithName("router"), c.Duration(FlagApiTimeout), c.Duration(FlagQueryTimeout)),
			BaseContext: func(_ net.Listener) context.Context {
				return c.Context
			},
		}

		group.Go(func() error {
			return srv.ListenAndServe()
		})

		group.Go(func() error {
			select {
			case <-ctx.Done():
			}
			return srv.Shutdown(ctx)
		})

		WatchKubeObjects(c.Context,
			bus,
			kc.CoreV1().Namespaces(),
			"ns")

		WatchKubeObjects(c.Context,
			bus,
			kc.StorageV1().StorageClasses(),
			"sc")

		WatchKubeObjects(c.Context,
			bus,
			kc.CoreV1().PersistentVolumes(),
			"pv")

		WatchKubeObjects(c.Context,
			bus,
			kc.CoreV1().Nodes(),
			"nodes")

		return group.Wait()
	}

	_ = app.RunContext(ctx, os.Args)
}

func newRouter(log logr.Logger, apiTimeout, queryTimeout time.Duration) *mux.Router {
	router := mux.NewRouter()

	router.Use(func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rCtx, cancel := context.WithTimeout(r.Context(), apiTimeout)
			defer cancel()

			h.ServeHTTP(w, r.WithContext(rCtx))
		})
	})

	router.HandleFunc("/inventory", func(w http.ResponseWriter, req *http.Request) {
		storage := StorageFromCtx(req.Context())
		inv := akashv1.Inventory{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Inventory",
				APIVersion: "akash.network/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				CreationTimestamp: metav1.NewTime(time.Now().UTC()),
			},
			Spec: akashv1.InventorySpec{},
			Status: akashv1.InventoryStatus{
				State: akashv1.InventoryStatePulled,
			},
		}

		ctx, cancel := context.WithTimeout(req.Context(), queryTimeout)
		datach := make(chan runner.Result, 1)
		var wg sync.WaitGroup

		wg.Add(len(storage))

		for idx := range storage {
			go func(idx int) {
				defer wg.Done()

				datach <- runner.NewResult(storage[idx].Query(ctx))
			}(idx)
		}

		go func() {
			defer cancel()
			wg.Wait()
		}()

	done:
		for {
			select {
			case <-ctx.Done():
				break done
			case res := <-datach:
				if res.Error() != nil {
					inv.Status.Messages = append(inv.Status.Messages, res.Error().Error())
				}

				if inventory, valid := res.Value().([]akashv1.InventoryClusterStorage); valid {
					inv.Spec.Storage = append(inv.Spec.Storage, inventory...)
				}
			}
		}

		data, err := json.Marshal(&inv)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			data = []byte(err.Error())
		} else {
			w.Header().Set("Content-Type", "application/json")
		}

		_, _ = w.Write(data)
	})

	return router
}

func loadKubeConfig(c *cli.Context) error {
	log := LogFromCtx(c.Context)

	var config *rest.Config
	configPath := c.Path(FlagKubeConfig)

	_, err := os.Stat(configPath)
	if err == nil && !c.Bool(FlagKubeInCluster) {
		config, err = clientcmd.BuildConfigFromFlags("", configPath)
	} else if err != nil && c.IsSet(FlagKubeConfig) {
		return err
	} else {
		log.Info("trying use incluster kube config")
		config, err = rest.InClusterConfig()
	}

	if err != nil {
		return err
	}

	log.Info("kube config loaded successfully")

	ContextSet(c, CtxKeyKubeConfig, config)

	return nil
}
