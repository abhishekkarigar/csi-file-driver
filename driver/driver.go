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

func (ns *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Volume ID is required")
	}
	if req.GetStagingTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "Staging target path is required")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability is required")
	}
	volumeID := req.GetVolumeId()
	stagingTargetPath := req.GetStagingTargetPath()
	volumeCapability := req.GetVolumeCapability()

	// Ensure the volume capability is for a filesystem
	if volumeCapability.GetBlock() != nil {
		return nil, status.Error(codes.InvalidArgument, "Block volume not supported")
	}
	if volumeCapability.GetMount() == nil {
		return nil, status.Error(codes.InvalidArgument, "Mount capability required for filesystem volume")
	}
	// Get filesystem type and mount options
	fsType := volumeCapability.GetMount().GetFsType()
	if fsType == "" {
		fsType = "nfs" // Default to NFS; adjust based on your storage backend
	}
	if fsType != "nfs" {
		return nil, status.Errorf(codes.InvalidArgument, "Unsupported fsType: %s, only 'nfs' is supported", fsType)
	}

	mountFlags := volumeCapability.GetMount().GetMountFlags()
	// Add default NFS mount options if none provided
	if len(mountFlags) == 0 {
		mountFlags = []string{"vers=4,soft,timeo=600,retrans=2"}
	}

	// Get volume attributes (e.g., NFS server and path)
	publishContext := req.GetPublishContext()
	sourcePath, ok := publishContext["sourcePath"] // e.g., "nfs-server:/export" or Azure File share
	if !ok {
		logrus.Errorf("NodeStageVolume: sourcePath not found in publish_context for volume %s", volumeID)
		return nil, status.Error(codes.InvalidArgument, "sourcePath not found in publish_context")
	}
	// Check if the staging path is already mounted
	if isMounted(stagingTargetPath) {
		logrus.Infof("NodeStageVolume: Volume %s already staged at %s", volumeID, stagingTargetPath)
		return &csi.NodeStageVolumeResponse{}, nil // Idempotent: return success if already staged
	}

	// Create the staging target path if it doesn't exist
	if err := os.MkdirAll(stagingTargetPath, 0755); err != nil {
		logrus.Errorf("NodeStageVolume: Failed to create staging path %s: %v", stagingTargetPath, err)
		return nil, status.Errorf(codes.Internal, "Failed to create staging path %s: %v", stagingTargetPath, err)
	}

	// Mount the filesystem to the staging target path
	logrus.Infof("NodeStageVolume: Mounting %s to %s (fsType: %s)", sourcePath, stagingTargetPath, fsType)
	if err := mountFilesystem(sourcePath, stagingTargetPath, fsType, mountFlags); err != nil {
		logrus.Errorf("NodeStageVolume: Failed to mount %s to %s: %v", sourcePath, stagingTargetPath, err)
		return nil, status.Errorf(codes.Internal, "Failed to mount %s to %s: %v", sourcePath, stagingTargetPath, err)
	}

	logrus.Infof("NodeStageVolume: Successfully staged volume %s to %s", volumeID, stagingTargetPath)
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
		return &csi.NodeUnstageVolumeResponse{}, nil // Idempotent: return success if not mounted
	}

	// Unmount the filesystem from the staging path
	logrus.Infof("NodeUnstageVolume: Unmounting volume %s from %s", volumeID, stagingTargetPath)
	if err := unmountFilesystem(stagingTargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to unmount %s: %v", stagingTargetPath, err)
	}

	// Optionally, remove the staging directory if empty
	if err := os.Remove(stagingTargetPath); err != nil && !os.IsNotExist(err) {
		logrus.Warningf("NodeUnstageVolume: Failed to remove staging path %s: %v", stagingTargetPath, err)
		// Not fatal; continue with success
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
	args := []string{sourcePath, targetPath}
	if fsType != "" {
		args = append([]string{"-t", fsType}, args...)
	}
	if len(mountFlags) > 0 {
		args = append([]string{"-o", mountFlags[0]}, args...)
	}
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
