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
	"os/exec"
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

	// volID is pvc-50e7a998-5559-447f-af1f-eba501ded56d
	// volPath /mnt/data/pvc-50e7a998-5559-447f-af1f-eba501ded56d
	// staging path is /mnt also called as mounting path or backing path
	// bind-mount is when you mount  this staging path to the target path in pod.
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
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
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

// ControllerPublishVolume provides the local directory path for the volume.
func (cs *controllerServer) ControllerPublishVolume(
	ctx context.Context,
	req *csi.ControllerPublishVolumeRequest,
) (*csi.ControllerPublishVolumeResponse, error) {

	volumeID := req.GetVolumeId()
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing")
	}

	// For file-based volumes, we can return a dummy devicePath
	publishContext := map[string]string{
		"devicePath": fmt.Sprintf("/mnt/data/%s", volumeID), // pseudo path for file-based volume
	}

	return &csi.ControllerPublishVolumeResponse{
		PublishContext: publishContext,
	}, nil
}

// ControllerUnpublishVolume (minimal implementation for cleanup)
func (cs *controllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}
	logrus.Infof("ControllerUnpublishVolume: Volume %s unpublished", req.GetVolumeId())
	return &csi.ControllerUnpublishVolumeResponse{}, nil
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

	// Create the source path if it doesn't exist
	if err := os.MkdirAll(src, 0755); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create source path %s: %v", src, err)
	}

	// Create the target path (where kubelet wants to mount)
	if err := os.MkdirAll(target, 0755); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create target path %s: %v", target, err)
	}

	// Perform the bind mount
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
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
		},
	}, nil
}

func (ns *nodeServer) NodeStageVolume(
	ctx context.Context,
	req *csi.NodeStageVolumeRequest,
) (*csi.NodeStageVolumeResponse, error) {

	devicePath := req.GetPublishContext()["devicePath"]
	stagingTargetPath := req.GetStagingTargetPath()

	if devicePath == "" {
		return nil, status.Error(codes.InvalidArgument, "devicePath missing in publish context")
	}

	// Validate that devicePath exists
	if stat, err := os.Stat(devicePath); err != nil || !stat.IsDir() {
		return nil, status.Errorf(codes.InvalidArgument, "devicePath %q does not exist or is not a directory", devicePath)
	}

	// Create the staging path
	if err := os.MkdirAll(stagingTargetPath, 0755); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create staging target path: %v", err)
	}

	// Perform the bind mount
	if err := unix.Mount(devicePath, stagingTargetPath, "", unix.MS_BIND, ""); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to bind mount devicePath (%s) to staging (%s): %v", devicePath, stagingTargetPath, err)
	}

	logrus.Infof("NodeStageVolume: Mounted %s to %s", devicePath, stagingTargetPath)
	return &csi.NodeStageVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	// Validate request parameters
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}
	if req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path is required")
	}

	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()

	// Check if the staging path is mounted
	if !isMounted(stagingTargetPath) {
		logrus.Infof("NodeUnstageVolume: Volume %s is not mounted at %s, nothing to do", volumeID, stagingTargetPath)
		return &csi.NodeUnstageVolumeResponse{}, nil // Idempotent
	}

	// Unmount the filesystem
	logrus.Infof("NodeUnstageVolume: Unmounting volume %s from %s", volumeID, stagingTargetPath)
	if err := unmountFilesystem(stagingTargetPath); err != nil {
		logrus.Errorf("NodeUnstageVolume: Failed to unmount %s: %v", stagingTargetPath, err)
		return nil, status.Errorf(codes.Internal, "Failed to unmount %s: %v", stagingTargetPath, err)
	}

	// Remove the staging directory if empty
	if err := os.Remove(stagingTargetPath); err != nil && !os.IsNotExist(err) {
		logrus.Warningf("NodeUnstageVolume: Failed to remove staging path %s: %v", stagingTargetPath, err)
	}

	logrus.Infof("NodeUnstageVolume: Successfully unstaged volume %s from %s", volumeID, stagingTargetPath)
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func unmountFilesystem(targetPath string) error {
	cmd := exec.Command("umount", targetPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to unmount filesystem: %v, output: %s", err, string(output))
	}
	return nil
}

// Helper functions
func mountFilesystem(sourcePath, targetPath, fsType string, mountFlags []string) error {
	args := []string{}
	if fsType != "" {
		args = append(args, "-t", fsType)
	}
	if len(mountFlags) > 0 {
		args = append(args, "-o", mountFlags[0])
	}
	args = append(args, sourcePath, targetPath)
	cmd := exec.Command("mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to mount filesystem: %v, output: %s", err, string(output))
	}
	return nil
}

func isMounted(targetPath string) bool {
	cmd := exec.Command("mountpoint", "-q", targetPath)
	return cmd.Run() == nil
}
