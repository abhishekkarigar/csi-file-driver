# csi-file-driver



#### kubectl apply -f csi-rbac.yaml  # (ServiceAccount, ClusterRole, ClusterRoleBinding)
#### kubectl apply -f csi-driver.yaml
#### kubectl apply -f csi-deployment.yaml # (with CSI Provisioner sidecar)
#### kubectl apply -f csi-node-daemonset.yaml (Node plugin for kubelet mount)
#### kubectl apply -f storageclass.yaml
#### kubectl apply -f pvc.yaml
#### kubectl apply -f pod.yaml


