# csi-file-driver



#### kubectl apply -f csi-rbac.yaml  # (ServiceAccount, ClusterRole, ClusterRoleBinding)
#### kubectl apply -f csi-driver.yaml
#### kubectl apply -f csi-deployment.yaml # (with CSI Provisioner sidecar)
#### kubectl apply -f csi-node-daemonset.yaml (Node plugin for kubelet mount)
#### kubectl apply -f storageclass.yaml
#### kubectl apply -f pvc.yaml
#### kubectl apply -f pod.yaml


# know how  


#### mount --bind /mnt/staging/ebs-vol-123 /var/lib/kubelet/pods/<pod-id>/volumes/ebs-vol
#### mount --bind /mnt/data/pvc-vol-123 /var/lib/kubelet/pods/fa77ae39-383c-4b6d-ad6e-7f0344d32955/volumes/kubernetes.io~csi/





