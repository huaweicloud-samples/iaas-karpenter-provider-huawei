[![GitHub License](https://img.shields.io/badge/License-Apache%202.0-ff69b4.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/HuaweiCloudDeveloper/karpenter-provider-huawei)](https://goreportcard.com/report/github.com/HuaweiCloudDeveloper/karpenter-provider-huawei)
[![contributions welcome](https://img.shields.io/badge/contributions-welcome-brightgreen.svg?style=flat)](https://github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/issues)

## Overview

The Huawei Karpenter Provider enables node autoprovisioning using [Karpenter](https://karpenter.sh/) on your CCE cluster.
Karpenter improves the efficiency and cost of running workloads on Kubernetes clusters by:

* **Watching** for pods that the Kubernetes scheduler has marked as unschedulable
* **Evaluating** scheduling constraints (resource requests, node selectors, affinities, tolerations, and topology-spread constraints) requested by the pods
* **Provisioning** nodes that meet the requirements of the pods
* **Removing** the nodes when they are no longer needed
* **Consolidating** existing nodes onto cheaper nodes with higher utilization per node

## Known Limitations

- **On-demand only**: Only `on-demand` capacity type is supported. Spot instances are not yet available.

## Prerequisites

- A **Huawei Cloud CCE cluster** running Kubernetes **1.26 - 1.36**
- Huawei Cloud credentials (AK/SK), or a CCE Pod Identity-associated IAM agency, with permissions for:
  - **ECS** - Elastic Cloud Server (flavor and availability zone discovery)
  - **CCE** - Cloud Container Engine (node create/delete/list)
  - **VPC** - Virtual Private Cloud (subnet discovery)
  - **BSS** - Billing (on-demand pricing queries)
- [Helm](https://helm.sh/) v3


## Installation

### 1. Install via Helm

#### Using AK/SK credentials

```bash
helm install karpenter-provider-huawei charts/karpenter-provider-huawei \
  --namespace karpenter-provider-huawei-system \
  --create-namespace \
  --set-string credentials.accessKey=<your-access-key> \
  --set-string credentials.secretKey=<your-secret-key> \
  --set-string credentials.region=<region-id> \
  --set-string clusterInfo.clusterID=<cce-cluster-id>
```

The chart creates a `huawei-credentials` Secret by default and loads it into the controller.
To use an existing Secret instead, set `credentials.create=false` and `credentials.existingSecret=<secret-name>`.

#### Using CCE Pod Identity

Create an IAM agency with the required permissions, then create the ServiceAccount used by Pod Identity:

```bash
kubectl create namespace karpenter-provider-huawei-system \
  --dry-run=client \
  -o yaml | kubectl apply -f -

kubectl create serviceaccount karpenter-provider-huawei-controller-manager \
  --namespace karpenter-provider-huawei-system \
  --dry-run=client \
  -o yaml | kubectl apply -f -
```

In the CCE console, [create a pod identity association](https://support.huaweicloud.com/usermanual-cce/cce_10_1110.html#section2):
- Namespace: `karpenter-provider-huawei-system`
- ServiceAccount: `karpenter-provider-huawei-controller-manager`
- IAM agency: `<agency-name>`

Wait for the association to become ready, then install the chart and reuse the existing ServiceAccount:

```bash
helm install karpenter-provider-huawei charts/karpenter-provider-huawei \
  --namespace karpenter-provider-huawei-system \
  --set serviceAccount.create=false \
  --set podIdentity.enabled=true \
  --set-string credentials.region=<region-id> \
  --set-string clusterInfo.clusterID=<cce-cluster-id>
```

Keep `serviceAccount.create=false` on future Helm upgrades. Do not set `credentials.accessKey` or `credentials.secretKey` when `podIdentity.enabled=true`.

## Getting Started

### Step 1: Create a CCENodeClass

`CCENodeClass` is a **cluster-scoped** resource that defines Huawei Cloud-specific node configuration:

```yaml
apiVersion: karpenter.k8s.huawei/v1alpha1
kind: CCENodeClass
metadata:
  name: default
spec:
  subnetSelectorTerms:
    - id: "<subnet-uuid>"                  # Your VPC subnet ID
  imsSelector:
    imsFamily: "Huawei Cloud EulerOS 2.0"  # Example value verified on a live CCE cluster
  blockDeviceMappings:
    k8s:
      volumeSize: 120
      volumeType: SAS
    root:
      volumeSize: 120
      volumeType: SAS
    users:
      - volumeSize: 100
        volumeType: SAS
  runtimeConfiguration:
    type: containerd
  login:
    userPassword:
      username: root
      password: "<salted-and-encrypted-password>"
```

To use an existing Huawei Cloud key pair instead of password login, set `login.sshKey`:

```yaml
apiVersion: karpenter.k8s.huawei/v1alpha1
kind: CCENodeClass
metadata:
  name: default
spec:
  subnetSelectorTerms:
    - id: "<subnet-uuid>"
  imsSelector:
    imsFamily: "Huawei Cloud EulerOS 2.0"
  blockDeviceMappings:
    root:
      volumeSize: 120
      volumeType: SAS
  login:
    sshKey: "<existing-keypair-name>"
```

After creation, wait for the `SubnetsReady` condition to become `True` before the NodeClass can be used for provisioning:

```bash
kubectl get ccenodeclass default -o jsonpath='{.status.conditions}'
```

### Step 2: Create a NodePool

Create a Karpenter `NodePool` that references your `CCENodeClass`:

```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: default
spec:
  template:
    spec:
      nodeClassRef:
        group: karpenter.k8s.huawei
        kind: CCENodeClass
        name: default
      requirements:
        - key: karpenter.sh/capacity-type
          operator: In
          values: ["on-demand"]
        - key: kubernetes.io/arch
          operator: In
          values: ["amd64"]
  disruption:
    consolidationPolicy: WhenEmptyOrUnderutilized
    consolidateAfter: 1m
```

### Step 3: Deploy a Workload

Deploy a workload with resource requests. Karpenter will automatically provision right-sized nodes:

```bash
kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: inflate
spec:
  replicas: 5
  selector:
    matchLabels:
      app: inflate
  template:
    metadata:
      labels:
        app: inflate
    spec:
      containers:
        - name: inflate
          image: nginx:latest
          resources:
            requests:
              cpu: "1"
              memory: 1Gi
EOF
```

## Development

### Prerequisites

- [Go](https://go.dev/) 1.25+
- [Docker](https://www.docker.com/) (or Podman)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [Helm](https://helm.sh/) v3
- [Kind](https://kind.sigs.k8s.io/) (for e2e tests)

### Build

```bash
make build                                              # Build controller binary
make docker-build IMG=<your-registry>/controller:<tag>  # Build Docker image
make docker-push IMG=<your-registry>/controller:<tag>   # Push Docker image
make docker-buildx IMG=<your-registry>/controller:<tag> # Cross-platform build
```

### Test

```bash
make test      # Unit tests
make test-e2e  # E2E tests (uses Kind)
```

### Lint

```bash
make lint      # Run golangci-lint
make lint-fix  # Run with auto-fix
```

### Code Generation

```bash
make manifests  # Generate CRD manifests
make generate   # Generate DeepCopy methods
```

### Helm

```bash
make helm-lint       # Lint the chart
make helm-package    # Package the chart and regenerate charts/index.yaml
make helm-template   # Render templates locally
make helm-install    # Install
make helm-upgrade    # Upgrade
make helm-uninstall  # Uninstall
```

## Roadmap

See [docs/ROADMAP.md](docs/ROADMAP.md) for the project roadmap and progress.

## Contributing

Contributions are welcome! Please feel free to submit issues and pull requests.

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/my-feature`)
3. Make your changes and ensure tests pass (`make test && make lint`)
4. Submit a Pull Request following the [PR template](.github/PULL_REQUEST_TEMPLATE.md)

## Community

- **Issues**: [GitHub Issues](https://github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/issues)
- **Karpenter Upstream**: [karpenter.sh](https://karpenter.sh/) | [Karpenter GitHub](https://github.com/kubernetes-sigs/karpenter)

## Code of Conduct

This project follows the [CNCF Community Code of Conduct](https://github.com/cncf/foundation/blob/master/code-of-conduct.md).

## License

This project is licensed under the [Apache License 2.0](LICENSE).
