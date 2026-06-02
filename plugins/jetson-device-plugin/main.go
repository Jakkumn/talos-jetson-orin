// jetson-device-plugin — minimal CDI-native GPU device plugin for Jetson Orin
//
// Exposes nvidia.com/gpu: N (configurable via REPLICA_COUNT env var, default 1)
// as a Kubernetes extended resource. On Allocate(), returns
// CDIDevices: [{Name: "nvidia.com/gpu=0"}] so that containerd 2.x reads
// /var/run/cdi/nvidia-jetson.yaml and automatically injects all nvgpu +
// nvhost device nodes, JetPack r36.5 lib bind-mount, and LD_LIBRARY_PATH
// into the container — no hostPath mounts needed.
//
// REPLICA_COUNT enables GPU time-slicing — the physical iGPU is advertised
// as N virtual slots, all backed by the same device + same CDI spec entry.
// Multiple pods schedule concurrently; the CUDA driver time-slices at runtime
// (same behavior as bare JetPack hosting multiple CUDA processes).
//
// GPU presence is detected by /dev/nvgpu/igpu0/ctrl (nvgpu 5.x, NVHOST=n).
//
// Build (linux/arm64):
//   docker buildx build --platform linux/arm64 -t REGISTRY/jetson-device-plugin:v1.1.0 .
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	resourceName = "nvidia.com/gpu"
	devicePath   = "/dev/nvgpu/igpu0/ctrl"
	cdiDevice    = "nvidia.com/gpu=0"
	kubeletSock  = "/var/lib/kubelet/device-plugins/kubelet.sock"
	pluginSock   = "/var/lib/kubelet/device-plugins/jetson-device-plugin.sock"
	pollInterval = 30 * time.Second
)

// JetsonPlugin implements the Kubernetes Device Plugin gRPC interface.
type JetsonPlugin struct {
	v1beta1.UnimplementedDevicePluginServer
	replicaCount int
}

func gpuHealth() string {
	if _, err := os.Stat(devicePath); err == nil {
		return v1beta1.Healthy
	}
	return v1beta1.Unhealthy
}

// GetDevicePluginOptions returns plugin options (no special options needed).
func (p *JetsonPlugin) GetDevicePluginOptions(_ context.Context, _ *v1beta1.Empty) (*v1beta1.DevicePluginOptions, error) {
	return &v1beta1.DevicePluginOptions{}, nil
}

// ListAndWatch reports replicaCount virtual GPU devices (all backed by the
// same physical Jetson iGPU via time-slicing) and re-reports health every
// pollInterval. All replicas share the same health state.
func (p *JetsonPlugin) ListAndWatch(_ *v1beta1.Empty, s v1beta1.DevicePlugin_ListAndWatchServer) error {
	devices := make([]*v1beta1.Device, p.replicaCount)
	for i := 0; i < p.replicaCount; i++ {
		devices[i] = &v1beta1.Device{
			ID:     fmt.Sprintf("igpu0-%d", i),
			Health: v1beta1.Healthy,
		}
	}

	for {
		health := gpuHealth()
		for _, d := range devices {
			d.Health = health
		}
		if err := s.Send(&v1beta1.ListAndWatchResponse{Devices: devices}); err != nil {
			return err
		}
		time.Sleep(pollInterval)
	}
}

// GetPreferredAllocation is optional — not needed for single-GPU nodes.
func (p *JetsonPlugin) GetPreferredAllocation(_ context.Context, _ *v1beta1.PreferredAllocationRequest) (*v1beta1.PreferredAllocationResponse, error) {
	return &v1beta1.PreferredAllocationResponse{}, nil
}

// Allocate returns:
//   - A sentinel DeviceSpec for /dev/nvgpu/igpu0/ctrl so kubelet tracks the device.
//   - CDIDevices: [{Name: "nvidia.com/gpu=0"}] so containerd 2.x reads
//     /var/run/cdi/nvidia-jetson.yaml and injects all GPU devices + libs.
//
// Safety guard: rejects requests for more than one GPU per container, since
// time-sliced replicas all back the same physical device — allocating multiple
// to a single container just hogs slots without providing extra compute.
func (p *JetsonPlugin) Allocate(_ context.Context, r *v1beta1.AllocateRequest) (*v1beta1.AllocateResponse, error) {
	var responses []*v1beta1.ContainerAllocateResponse
	for _, req := range r.ContainerRequests {
		if len(req.DevicesIds) > 1 {
			return nil, fmt.Errorf("requesting more than 1 %s per container is not supported (time-sliced single GPU)", resourceName)
		}
		responses = append(responses, &v1beta1.ContainerAllocateResponse{
			Devices: []*v1beta1.DeviceSpec{{
				ContainerPath: devicePath,
				HostPath:      devicePath,
				Permissions:   "rw",
			}},
			// CDI injection: containerd reads the spec at /var/run/cdi/nvidia-jetson.yaml
			// and injects all nvgpu + nvhost devices, tegra lib bind-mount, LD_LIBRARY_PATH.
			CdiDevices: []*v1beta1.CDIDevice{{
				Name: cdiDevice,
			}},
		})
	}
	return &v1beta1.AllocateResponse{ContainerResponses: responses}, nil
}

// PreStartContainer is a no-op for this plugin.
func (p *JetsonPlugin) PreStartContainer(_ context.Context, _ *v1beta1.PreStartContainerRequest) (*v1beta1.PreStartContainerResponse, error) {
	return &v1beta1.PreStartContainerResponse{}, nil
}

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)

	replicaCount := 1
	if v := os.Getenv("REPLICA_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			replicaCount = n
		}
	}
	log.Printf("jetson-device-plugin starting: resource=%s cdi=%s replicas=%d",
		resourceName, cdiDevice, replicaCount)

	// Clean up any stale socket from previous run.
	_ = os.Remove(pluginSock)

	lis, err := net.Listen("unix", pluginSock)
	if err != nil {
		log.Fatalf("listen %s: %v", pluginSock, err)
	}

	srv := grpc.NewServer()
	v1beta1.RegisterDevicePluginServer(srv, &JetsonPlugin{replicaCount: replicaCount})
	go func() {
		if err := srv.Serve(lis); err != nil {
			log.Fatalf("gRPC server error: %v", err)
		}
	}()
	log.Printf("gRPC server listening on %s", pluginSock)

	// Register with kubelet.
	conn, err := grpc.NewClient(
		"unix://"+kubeletSock,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("dial kubelet: %v", err)
	}
	defer conn.Close()

	regClient := v1beta1.NewRegistrationClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := regClient.Register(ctx, &v1beta1.RegisterRequest{
		Version:      v1beta1.Version,
		Endpoint:     filepath.Base(pluginSock),
		ResourceName: resourceName,
	}); err != nil {
		log.Fatalf("register with kubelet: %v", err)
	}
	log.Printf("registered with kubelet as %s", resourceName)

	// Block until signal.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down")
	srv.GracefulStop()
}
