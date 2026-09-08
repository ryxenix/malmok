package storage

import (
	"strings"

	"github.com/ryxenix/malmok/api/v1alpha1"
)

// Manifest renders Rancher's local-path-provisioner.
//
// It is upstream's deploy/local-path-storage.yaml for the pinned release, with
// three deliberate differences:
//
//   - The helper image is pinned. Upstream names `docker.io/library/busybox`
//     with no tag, and an untagged image is :latest -- which no bundle can
//     carry, because what it refers to changes. The helper pod runs on every
//     volume create and delete, so an image that cannot be pulled is a volume
//     that is never made and never removed.
//   - The StorageClass carries the default-class annotation when the document
//     asks for it, which upstream leaves off. A claim that names no class and
//     finds no default waits forever, and the components installed here name
//     no class.
//   - It is rendered rather than fetched, so an air-gapped site carries two
//     images and nothing else.
//
// Rendered from Go rather than embedded verbatim because the parameters above
// have to be substituted, and a template with three holes in it is harder to
// read than the thing itself.
func Manifest(spec v1alpha1.ClusterSpec) string {
	var b strings.Builder
	b.WriteString(managedFileHeader + "\n")
	b.WriteString("# Rancher local-path-provisioner " + ProvisionerVersion + "\n")

	b.WriteString(`---
apiVersion: v1
kind: Namespace
metadata:
  name: ` + Namespace + `
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: local-path-provisioner-service-account
  namespace: ` + Namespace + `
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: local-path-provisioner-role
  namespace: ` + Namespace + `
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch", "create", "patch", "update", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: local-path-provisioner-role
rules:
  - apiGroups: [""]
    resources: ["nodes", "persistentvolumeclaims", "configmaps", "pods", "pods/log"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["persistentvolumes"]
    verbs: ["get", "list", "watch", "create", "patch", "update", "delete"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
  - apiGroups: ["storage.k8s.io"]
    resources: ["storageclasses"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: local-path-provisioner-bind
  namespace: ` + Namespace + `
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: local-path-provisioner-role
subjects:
  - kind: ServiceAccount
    name: local-path-provisioner-service-account
    namespace: ` + Namespace + `
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: local-path-provisioner-bind
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: local-path-provisioner-role
subjects:
  - kind: ServiceAccount
    name: local-path-provisioner-service-account
    namespace: ` + Namespace + `
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: local-path-provisioner
  namespace: ` + Namespace + `
spec:
  replicas: 1
  selector:
    matchLabels:
      app: local-path-provisioner
  template:
    metadata:
      labels:
        app: local-path-provisioner
    spec:
      serviceAccountName: local-path-provisioner-service-account
      # The provisioner has to run somewhere that is up before the workloads
      # that need volumes are scheduled, and on a single-node cluster the only
      # node carries the control-plane taints.
      tolerations:
        - key: node-role.kubernetes.io/control-plane
          operator: Exists
          effect: NoSchedule
        - key: node-role.kubernetes.io/etcd
          operator: Exists
          effect: NoExecute
      containers:
        - name: local-path-provisioner
          image: ` + ProvisionerImage + `
          imagePullPolicy: IfNotPresent
          command:
            - local-path-provisioner
            - --debug
            - start
            - --config
            - /etc/config/config.json
          volumeMounts:
            - name: config-volume
              mountPath: /etc/config/
          env:
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
            - name: CONFIG_MOUNT_PATH
              value: /etc/config/
      volumes:
        - name: config-volume
          configMap:
            name: local-path-config
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ` + ClassName + `
`)

	if IsDefaultClass(spec) {
		b.WriteString(`  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
`)
	}

	// WaitForFirstConsumer, because a local-path volume is a directory on one
	// node: binding before the scheduler has chosen a node would pick the
	// wrong one whenever the pod cannot run there.
	//
	// Delete, because these volumes hold what the cluster can rebuild. A site
	// that wants otherwise creates its own class; this one is the default and
	// a default that leaks directories on every node is worse.
	b.WriteString(`provisioner: rancher.io/local-path
volumeBindingMode: WaitForFirstConsumer
reclaimPolicy: Delete
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-path-config
  namespace: ` + Namespace + `
data:
  config.json: |-
    {
      "nodePathMap": [
        {
          "node": "DEFAULT_PATH_FOR_NON_LISTED_NODES",
          "paths": ["` + DataPath + `"]
        }
      ]
    }
  setup: |-
    #!/bin/sh
    set -eu
    mkdir -m 0777 -p "$VOL_DIR"
  teardown: |-
    #!/bin/sh
    set -eu
    rm -rf "$VOL_DIR"
  helperPod.yaml: |-
    apiVersion: v1
    kind: Pod
    metadata:
      name: helper-pod
    spec:
      priorityClassName: system-node-critical
      tolerations:
        - key: node.kubernetes.io/disk-pressure
          operator: Exists
          effect: NoSchedule
      containers:
        - name: helper-pod
          image: ` + HelperImage + `
          imagePullPolicy: IfNotPresent
`)

	return b.String()
}
