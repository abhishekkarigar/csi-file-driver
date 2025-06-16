package driver

import (
	"context"
	"fmt"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net"
	"os"
)

// FSDriver is the main struct that holds driver-level config
type FSDriver struct {
	name     string
	version  string
	endpoint string
	nodeID   string
}

// NewDriver creates a new FSDriver instance
func NewDriver(name, version, endpoint string) (*FSDriver, error) {
	return &FSDriver{
		name:     name,
		version:  version,
		endpoint: endpoint,
		nodeID:   "k3s-node", // can be passed in if needed
	}, nil
}

// Run starts the gRPC server and registers CSI services
func (d *FSDriver) Run() {
	socket := fmt.Sprintf("%s/csi.sock", d.endpoint)
	_ = os.RemoveAll(socket)

	listener, err := net.Listen("unix", socket)
	if err != nil {
		logrus.Fatalf("Failed to listen on socket %s: %v", socket, err)
	}

	server := grpc.NewServer()
	csi.RegisterIdentityServer(server, &identityServer{driver: d})
	csi.RegisterControllerServer(server, &controllerServer{driver: d})
	csi.RegisterNodeServer(server, &nodeServer{driver: d})

	logrus.Infof("CSI driver %s running at %s", d.name, socket)
	if err := server.Serve(listener); err != nil {
		logrus.Fatalf("gRPC server failed: %v", err)
	}
}

////////////////////////////////////////////////////////////////////////////////
// Identity Service

type identityServer struct {
	csi.UnimplementedIdentityServer
	driver *FSDriver
}

func (ids *identityServer) GetPluginInfo(context.Context, *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{
		Name:          ids.driver.name,
		VendorVersion: ids.driver.version,
	}, nil
}

func (ids *identityServer) GetPluginCapabilities(context.Context, *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
					},
				},
			},
		},
	}, nil
}

func (ids *identityServer) Probe(context.Context, *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{}, nil
}

////////////////////////////////////////////////////////////////////////////////
// Controller Service

type controllerServer struct {
	csi.UnimplementedControllerServer
	driver *FSDriver
}

func (cs *controllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	volID := req.GetName()
	volPath := fmt.Sprintf("/mnt/data/%s", volID)

	if err := os.MkdirAll(volPath, 0777); err != nil {
		return nil, fmt.Errorf("failed to create volume directory: %v", err)
	}

	logrus.Infof("Created volume %s at path %s", volID, volPath)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volID,
			CapacityBytes: 1 << 30, // 1GiB default
			VolumeContext: map[string]string{"path": volPath},
		},
	}, nil
}

func (cs *controllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	volID := req.GetVolumeId()
	volPath := fmt.Sprintf("/mnt/data/%s", volID)

	if err := os.RemoveAll(volPath); err != nil {
		logrus.Warnf("Failed to remove volume path %s: %v", volPath, err)
	}

	logrus.Infof("Deleted volume %s", volID)
	return &csi.DeleteVolumeResponse{}, nil
}

func (cs *controllerServer) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: []*csi.ControllerServiceCapability{
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					},
				},
			},
		},
	}, nil
}

func (cs *controllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.VolumeCapabilities,
		},
	}, nil
}

////////////////////////////////////////////////////////////////////////////////
// Node Service

type nodeServer struct {
	csi.UnimplementedNodeServer
	driver *FSDriver
}

func (ns *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: ns.driver.nodeID,
	}, nil
}

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	target := req.GetTargetPath()
	volID := req.GetVolumeId()
	src := fmt.Sprintf("/mnt/data/%s", volID)

	if err := os.MkdirAll(src, 0755); err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to create target path %s", err)
	}

	// Ensure src exists
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil, status.Errorf(codes.NotFound, "source path %s does not exist", src)
	}

	// Bind mount (preferred over symlink)
	if err := unix.Mount(src, target, "", unix.MS_BIND, ""); err != nil {
		return nil, status.Errorf(codes.Internal, "bind mount failed: %v", err)
	}

	logrus.Infof("Mounted %s to %s", src, target)
	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	target := req.GetTargetPath()
	if err := os.RemoveAll(target); err != nil {
		logrus.Warnf("Failed to unpublish %s: %v", target, err)
	}
	logrus.Infof("Unmounted %s", target)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{},
	}, nil
}
