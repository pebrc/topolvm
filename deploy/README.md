How to deploy on your Kubernetes
================================

All components except for `lvmd` can deploy as Kubernetes pods.  `lvmd` need to be run on host operating system to manage LVM logical volumes on a volume group.  Refer to the following diagram to see how components run.

![component diagram](http://www.plantuml.com/plantuml/svg/fLLFRne_5Bplfx389R_3AegFEVmK5NgeH2C4QgeguM3ifyN2Qw_yXxIg-EwrTzVsiWH8rHlxtdZyPkmnZyOIRLqjYeRG7Qa0JMRG2FMh1cadw7U1qCjKIQkL4A0NmbLShX4nQ39TVK6vWrQWzvp2gxobXfTMDKhiw_ycwEO72A7U0eynXZEWH9kE8G0RhVRSS2L1lyfG8DOIkWNjLowut1M7ef2A0NfI3ExRPUslC9hdZ4FFLbrlHg1MSWNzw7xJWEx6lizpX-BrYSDobcQ-pqDhgBYnciGXEwXV3LPp6f7fUqJPxnHKzSY-KeRI4EorU_pERK20xR7zbuVDURMrduI35ZL__ihonYpJ30t4oK1yOY2-QY3-DmFnXmt47pOG_uM1-Bg1-8A1yR8l197GAUaAg0cLFYkauSRR0dezu0yDGxV0d3XfC6B9XXX04x2K9TUdoratornLd1Bnh8IhuTQNHmR70tpAuOWawVYMO9JJD5ot7A0MSZZcmDSvSER0cUCGN8eq1bZxX0JWwMjYe6DOHKFGvvyM90iFm6qyoEJMGEqXRR1LQdTfYvubm8xlHwWS7MpEB2fVRZR2mPgfDrd-ZzeyeGTKBHVJ8vXhVFV8rMAOwCGpeiWnEWk9GK_zk5LQ-701buCMSNbi_9uwVA8ElwCE3zLbdamnKdSM4bDuJjqLO9Q7expn_n8gaS-7fvbg81RklXDBjwF3SKq4jTsxRmtpq976Cw0YtShU9mD5p7ii3QwUnwUPKTaRd_33PdPiB2bwyWYIkLhy0G00)

You can find detailed descriptions for each component in [docs](../docs/) directory.  

Sample manifests are located at [deploy/manifests](./manifests/), which requires [cert-manager](https://github.com/jetstack/cert-manager).  After `lvmd` is setup and run on Node operating systems, you can apply these manifests:

```console
kubectl apply -f deploy/manifests/namespace.yaml
kubectl apply -f deploy/manifests
```

Note that `topolvm-shceduler` [manifest](./manifests/scheduler.yaml) is just an example for clusters running control plane on nodes.  It deploys `topolvm-scheduler` as `DaemonSet` and access it via host network.  If the control plane of your cluster does not run on nodes, you need to tweak YAML to deploy `topolvm-scheduler` using Deployment and Service and change `urlPrefix` in [scheduler-policy.cfg](./scheduler-config/scheduler-policy.cfg).

The rest of this document describes the deployment procedure in detail.

Custom Resource and Storage Class Definition
--------------------------------------------

TopoLVM uses the custom resource definition of `LogicalVolume` ([manifest](./manifests/crd.yaml)) and the storage class whose provisioner is `topolvm.cybozu.com` ([manifest](./manifests/provisioner.yaml)).

Components for each Node
------------------------

The components for each Node are:
1. lvmd
2. lvmetrics
3. topolvm-node
4. topolvm-csi (mode: node)

### lvmd

`lvmd` creates/deletes LVM logical volume using given Volume Group. You can get the runnable binary at [release page](https://github.com/cybozu-go/topolvm/releases) (you can also build it with command `go build pkg/lvmd`).  Then, run it with following commands:
```console
mkidr -p /run/topolvm
systemd-run --unit=lvmd.service lvmd --volume-group=<volume_group_name> --listen=/run/topolvm/lvmd.sock --spare=1
```
Now `lvmd.service` is running and open its API endpoint at Unix Domain Socket `/run/topolvm/lvmd.sock`.  Of course, you cau write systemd service definition like [this](./systemd/lvmd.service).

Note: If you do not have any Volume Group, you can use loopback device for testing.
```console
sudo truncate --size=20G build/backing_store
sudo losetup -f build/backing_store
sudo vgcreate -y myvg $(sudo losetup -j build/backing_store | cut -d: -f1)
```

### lvmetrics

`lvmetrics` gathers metrics from `lvmd` and annotate Kubernetes `Node` with them. So you must give the API endpoint of `lvmd` and which `Node` to annotate.
```yaml
- name: lvmetrics
 image: quay.io/cybozu/topolvm:0.1.0
 command:
 - /lvmetrics
 - --socket=/run/topolvm/lvmd.sock
 env:
 - name: NODE_NAME
 valueFrom:
 fieldRef:
 fieldPath: spec.nodeName
 volumeMounts:
 - name: lvmd-socket-dir
 mountPath: /run/topolvm
```

### topolvm-node

`topolvm-node` is a Kubernetes custom controller for Custom Resource `LogicalVolume`.  It controls logical volume via `lvmd`, so you must give the API endpoint of `lvmd`.
```yaml
- name: topolvm-node
 image: quay.io/cybozu/topolvm:0.1.0
 command:
 - /topolvm-node
 - --node-name=$(MY_NODE_NAME)
 - --lvmd-socket=/run/lvmd/lvmd.sock
 env:
 - name: MY_NODE_NAME
 valueFrom:
 fieldRef:
 fieldPath: spec.nodeName
 volumeMounts:
 - name: lvmd-socket-dir
 mountPath: /run/lvmd
```

### csi-topolvm (mode: node)

`csi-topolvm` is an Unified CSI Driver for TopoLVM. With option `node`, it works as Node Service. To obtain logical volume information, `csi-topolvm node` need the API endpoint of `lvmd`. It also open own API endpoints with given Unix Domain Socket path. This API endpoint is registered to kubelet by registrar container `csi-registrar`, which uses [kubernetes-csi/node-driver-registrar](https://github.com/kubernetes-csi/node-driver-registrar).
```yaml
- name: node
 image: quay.io/cybozu/topolvm:0.1.0
 securityContext:
 privileged: true
 command:
 - /csi-topolvm
 - node
 - --node-name=$(MY_NODE_NAME)
 - --csi-socket-name=/run/topolvm/csi-topolvm.sock
 - --lvmd-socket-name=/run/lvmd/lvmd.sock
 env:
 - name: MY_NODE_NAME
 valueFrom:
 fieldRef:
 fieldPath: spec.nodeName
 volumeMounts:
 - name: node-plugin-dir
 mountPath: /run/topolvm
 - name: lvmd-socket-dir
 mountPath: /run/lvmd
 - name: pod-volumes-dir
 mountPath: /var/lib/kubelet/pods
 mountPropagation: "Bidirectional"
 - name: csi-plugin-dir
 mountPath: /var/lib/kubelet/plugins/kubernetes.io/csi
 mountPropagation: "Bidirectional"

- name: csi-registrar
 image: quay.io/cybozu/topolvm:0.1.0
 command:
 - /csi-node-driver-registrar
 - "--csi-address=/run/topolvm/csi-topolvm.sock"
 - "--kubelet-registration-path=/var/lib/kubelet/plugins/topolvm.cybozu.com/node/csi-topolvm.sock"
 lifecycle:
 preStop:
 exec:
 command: ["/bin/sh", "-c", "rm -rf /registration/topolvm.cybozu.com /registration/topolvm.cybozu.com-reg.sock"]
 volumeMounts:
 - name: node-plugin-dir
 mountPath: /run/topolvm
 - name: registration-dir
 mountPath: /registration
```


Components for Control Plane
----------------------------

The components for Control Plane are:
1. topolvm-hook
2. topolvm-scheduler
3. topolvm-csi (mode: controller)

### topolvm-hook

`topolvm-hook` is a Kubernetes mutating admission webhook for TopoLVM. It reads requested resource size in `PersistentVolumeClaim` and add it to spec of containers, which referred by `csi-topolvm controller` to manage volume group capacity.
```yaml
- name: topolvm-hook
 image: quay.io/cybozu/topolvm:0.1.0
 command:
 - /topolvm-hook
 - --listen=:9252
 - --cert=/certs/tls.crt
 - --key=/certs/tls.key
 livenessProbe:
 httpGet:
 path: /status
 port: 9252
 scheme: HTTPS
 volumeMounts:
 - name: certs
 mountPath: /certs
```

### topolvm-scheduler

`topolvm-scheduler` is a Kubernetes scheduler extender for TopoLVM. It filters and prioritizes Nodes based on the amount of free space in their volume groups.  The API endpoint of `topolvm-scheduler` is accessed by Kubernetes API server.  You can deploy it anywhere as long as API server can access it.  The [configuration](./scheduler/scheduler-config.yaml) for TopoLVM must be applied to `kube-scheduler` with `--config` option.

### topolvm-csi (mode: controller)

`topolvm-csi` with `controller` option is Controller Service of CSI Driver. It opens the API Endpoint with Unix Domain Socket and receives requirements for logical volume from `csi-provisioner` and `csi-attacher` containers, which use [kubernetes-csi/external-provisioner](https://github.com/kubernetes-csi/external-provisioner) and [kubernetes-csi/external-attacher](https://github.com/kubernetes-csi/external-attacher).
```yaml
- name: controller
 image: quay.io/cybozu/topolvm:0.1.0
 command:
 - /csi-topolvm
 - controller
 - --csi-socket-name=/run/topolvm/csi-topolvm.sock
 volumeMounts:
 - name: socket-dir
 mountPath: /run/topolvm

- name: csi-provisioner
 image: quay.io/cybozu/topolvm:0.1.0
 command:
 - /csi-provisioner
 - "--csi-address=/run/topolvm/csi-topolvm.sock"
 - "--feature-gates=Topology=true"
 volumeMounts:
 - name: socket-dir
 mountPath: /run/topolvm

- name: csi-attacher
 image: quay.io/cybozu/topolvm:0.1.0
 command:
 - /csi-attacher
 - "--csi-address=/run/topolvm/csi-topolvm.sock"
 volumeMounts:
 - name: socket-dir
 mountPath: /run/topolvm
```
