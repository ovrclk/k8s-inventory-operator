package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cskr/pubsub"
	akashv1 "github.com/ovrclk/akash/pkg/apis/akash.network/v1"
	"github.com/ovrclk/akash/util/runner"
	rookv1 "github.com/rook/rook/pkg/apis/ceph.rook.io/v1"
	rookclientset "github.com/rook/rook/pkg/client/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/watch"
)

type stats struct {
	TotalBytes         uint64  `json:"total_bytes"`
	TotalAvailBytes    uint64  `json:"total_avail_bytes"`
	TotalUsedBytes     uint64  `json:"total_used_bytes"`
	TotalUsedRawBytes  uint64  `json:"total_user_raw_bytes"`
	TotalUsedRawRatio  float64 `json:"total_used_raw_ration"`
	NumOSDs            uint64  `json:"num_osds"`
	NumPerPoolOSDs     uint64  `json:"num_per_pool_osds"`
	NumPerPoolOmapOSDs uint64  `json:"num_per_pool_omap_osds"`
}

type statsClass struct {
	TotalBytes        uint64  `json:"total_bytes"`
	TotalAvailBytes   uint64  `json:"total_avail_bytes"`
	TotalUsedBytes    uint64  `json:"total_used_bytes"`
	TotalUsedRawBytes uint64  `json:"total_user_raw_bytes"`
	TotalUsedRawRatio float64 `json:"total_used_raw_ration"`
}

type poolStats struct {
	Name  string `json:"name"`
	ID    int    `json:"id"`
	Stats struct {
		Stored      uint64  `json:"stored"`
		Objects     uint64  `json:"objects"`
		KbUsed      uint64  `json:"kb_used"`
		BytesUsed   uint64  `json:"bytes_used"`
		PercentUsed float64 `json:"percent_used"`
		MaxAvail    uint64  `json:"max_avail"`
	} `json:"stats"`
}

type dfResp struct {
	Stats        stats                 `json:"stats"`
	StatsByClass map[string]statsClass `json:"stats_by_class"`
	Pools        []poolStats           `json:"pools"`
}

type cephClusters map[string]string

type storageClass struct {
	pool      string
	clusterID string
}

type storageClasses map[string]storageClass

func (sc storageClasses) dup() storageClasses {
	res := make(storageClasses, len(sc))

	for class, params := range sc {
		res[class] = params
	}

	return res
}

func (cc cephClusters) dup() cephClusters {
	res := make(cephClusters, len(cc))

	for id, ns := range cc {
		res[id] = ns
	}

	return res
}

type resp struct {
	res []akashv1.InventoryClusterStorage
	err error
}

type req struct {
	resp chan resp
}

type ceph struct {
	pubsub *pubsub.PubSub
	exe    RemotePodCommandExecutor
	rc     *rookclientset.Clientset
	ctx    context.Context
	cancel context.CancelFunc
	reqch  chan req
}

func NewCeph(ctx context.Context) (Storage, error) {
	ctx, cancel := context.WithCancel(ctx)

	c := &ceph{
		exe:    NewRemotePodCommandExecutor(KubeConfigFromCtx(ctx), KubeClientFromCtx(ctx)),
		rc:     RookClientFromCtx(ctx),
		pubsub: PubSubFromCtx(ctx),
		ctx:    ctx,
		cancel: cancel,
		reqch:  make(chan req, 100),
	}

	group := ErrGroupFromCtx(ctx)
	group.Go(c.run)

	return c, nil
}

func (c *ceph) Query() ([]akashv1.InventoryClusterStorage, error) {
	r := req{
		resp: make(chan resp, 1),
	}

	c.reqch <- r

	rsp := <-r.resp

	return rsp.res, rsp.err
}

func (c *ceph) run() error {
	events := make(chan interface{}, 1000)

	defer c.pubsub.Unsub(events)
	c.pubsub.AddSub(events, "ns", "sc")

	log := LogFromCtx(c.ctx).WithName("rook-ceph")

	clusters := make(cephClusters)
	scs := make(storageClasses)

	var pendingReq []req

	var scrapech <-chan runner.Result

	for {
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case rawEvt := <-events:
			switch evt := rawEvt.(type) {
			case watch.Event:
				msg := fmt.Sprintf("%8s monitoring %s", evt.Type, evt.Object.GetObjectKind().GroupVersionKind().Kind)

			evtdone:
				switch obj := evt.Object.(type) {
				case *corev1.Namespace:
					topic := "ns/" + obj.Name + "/cephclusters"
					switch evt.Type {
					case watch.Added:
						fallthrough
					case watch.Modified:
						WatchKubeObjects(c.ctx, c.pubsub, c.rc.CephV1().CephClusters(obj.Name), topic)
						c.pubsub.AddSub(events, topic)
					case watch.Deleted:
						c.pubsub.Unsub(events, "ns/"+obj.Name+"/cephclusters")
					default:
						break evtdone
					}

					log.Info(msg, "name", obj.Name)
				case *storagev1.StorageClass:
					// we're not interested in storage classes provisioned by provisioners other than ceph
					if !strings.HasSuffix(obj.Provisioner, ".csi.ceph.com") {
						log.Info("ignoring StorageClass", "name", obj.Name)
						break evtdone
					}

					switch evt.Type {
					case watch.Added:
						fallthrough
					case watch.Modified:
						sc := storageClass{}

						var exists bool
						if sc.pool, exists = obj.Parameters["pool"]; !exists {
							log.Info("StorageClass does not have \"pool\" parameter set", "StorageClass", obj.Name)
							delete(scs, obj.Name)
							break evtdone
						}

						if sc.clusterID, exists = obj.Parameters["clusterID"]; !exists {
							log.Info("StorageClass does not have \"clusterID\" parameter set", "StorageClass", obj.Name)
							delete(scs, obj.Name)
							break evtdone
						}

						scs[obj.Name] = sc
					case watch.Deleted:
						delete(scs, obj.Name)
					default:
						break evtdone
					}

					log.Info(msg, "name", obj.Name)
				case *rookv1.CephCluster:
					switch evt.Type {
					case watch.Added:
						fallthrough
					case watch.Modified:
						// add only clusters in with State == Created
						if obj.Status.State == rookv1.ClusterStateCreated {
							clusters[obj.Name] = obj.Namespace
							log.Info(msg, "ns", obj.Namespace, "name", obj.Name)
						}
					case watch.Deleted:
						log.Info(msg, "ns", obj.Namespace, "name", obj.Name)
						delete(clusters, obj.Name)
					}
				}
			}
		case req := <-c.reqch:
			pendingReq = append(pendingReq, req)
			if scrapech == nil {
				scrapech = runner.Do(func() runner.Result {
					return runner.NewResult(c.scrapeMetrics(scs.dup(), clusters.dup()))
				})
			}
		case res := <-scrapech:
			scrapech = nil

			for _, r := range pendingReq {
				r.resp <- resp{
					res: res.Value().([]akashv1.InventoryClusterStorage),
					err: res.Error(),
				}
			}

			pendingReq = []req{}
		}
	}
}

func (c *ceph) scrapeMetrics(scs storageClasses, clusters map[string]string) ([]akashv1.InventoryClusterStorage, error) {
	var res []akashv1.InventoryClusterStorage

	dfResults := make(map[string]dfResp, len(clusters))

	for clusterID, ns := range clusters {
		stdout, _, err := c.exe.ExecCommandInContainerWithFullOutput("rook-ceph-tools", "rook-ceph-tools", ns, "ceph", "df", "--format", "json")
		if err != nil {
			return nil, err
		}

		rsp := dfResp{}

		if err = json.Unmarshal([]byte(stdout), &rsp); err != nil {
			return nil, err
		}

		dfResults[clusterID] = rsp
	}

	for class, params := range scs {
		df, exists := dfResults[params.clusterID]
		if !exists {
			continue
		}

		for _, pool := range df.Pools {
			if pool.Name == params.pool {
				res = append(res, akashv1.InventoryClusterStorage{
					Class:     class,
					ResourcePair: akashv1.ResourcePair{
						Allocated:   pool.Stats.BytesUsed,
						Allocatable: pool.Stats.MaxAvail,
					},
				})
				break
			}
		}
	}

	return res, nil
}
