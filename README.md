# kubernetes-console

This Go-based tool provides a simple way to:
- Export Kubernetes deployment details into a CSV file.
- Patch Kubernetes resource specifications from a CSV file.
- Restart deployments or update HPAs.

---

## Prerequisites
Before using the tool, ensure the following are installed:
- **Golang**: Version 1.20+
- **kubectl**: Installed and configured to access your cluster.
- **kubectx**: For switching between clusters.
- **kubens**: For switching between namespaces.

---

## How to Use This Tool

```bash
git clone https://github.com/hendralw/kubernetes-console.git
cd kubernetes-console
go mod tidy
```

---

## Steps
1. Switch to a Specific Cluster
   
```bash
kubectx <cluster-name>
```

2. Switch to a Specific Namespace

```bash
kubens <namespace-name>
```

3. Build the Go Script / Run the Go Script

```bash
go build -o main main.go
./kubernetes-console
```
```bash
go run main.go
```

---

This complete version contains all necessary instructions, including how to switch clusters and namespaces using `kubectx` and `kubens`, how to use the Go-based tool, and troubleshooting tips. Let me know if any further adjustments are needed!


