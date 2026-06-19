# Simple-Cache Operator Helm Chart

Deploys the Simple-Cache Kubernetes Operator for automated management of Simple-Cache distributed cache clusters.

## Prerequisites

- Kubernetes 1.25+
- Helm 3.0+

## Installation

```bash
# Add the Helm repository
helm repo add simple-cache https://lushenle.github.io/simple-cache
helm repo update

# Install the operator
helm install simple-cache-operator simple-cache/simple-cache-operator

# Or with custom values
helm install simple-cache-operator simple-cache/simple-cache-operator \
  --set operator.image.tag=v0.2.0 \
  --set createDefaultCluster=true
```

## Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `operator.replicas` | Number of operator replicas | `1` |
| `operator.image.repository` | Operator image repository | `ishenle/simple-cache-operator` |
| `operator.image.tag` | Operator image tag | `v0.1.0` |
| `operator.resources.requests.cpu` | CPU request | `100m` |
| `operator.resources.requests.memory` | Memory request | `64Mi` |
| `operator.resources.limits.cpu` | CPU limit | `500m` |
| `operator.resources.limits.memory` | Memory limit | `256Mi` |
| `createDefaultCluster` | Create a default CacheCluster instance | `false` |
| `rbac.create` | Create RBAC resources | `true` |
| `leaderElection.enabled` | Enable leader election | `false` |

## Creating a CacheCluster

After installing the operator, create a `CacheCluster` custom resource:

```yaml
apiVersion: cache.shenle.lu/v1
kind: CacheCluster
metadata:
  name: my-cache
spec:
  replicas: 3
  storage:
    size: 10Gi
```

Apply it:

```bash
kubectl apply -f my-cache.yaml
```

Check the cluster status:

```bash
kubectl get cacheclusters
kubectl describe cachecluster my-cache
```
