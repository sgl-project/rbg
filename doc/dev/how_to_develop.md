# Developer Guide

## Requirements

- Git
- Golang (version >= 1.24)
- Kubernetes (version >= 1.28)
- Docker or other container runtime
- GNU Make

For installation of Golang, please refer to [Install Golang](https://golang.org/dl/)

`make` is usually in a `build-essential` package in your distribution's package manager of choice. Make sure you have `make` on your machine.

There're great chances that you may want to run your implementation in a real Kubernetes cluster, so probably a Docker is needed for some necessary operations like building images.
See [Install Docker](https://docs.docker.com/engine/install/) for more information.

## How to Build, Run and Debug

### Get Source Code

We assume you already have a GitHub account.  

1. **Fork the RBG repository**  

    Click the **"Fork"** button on the [RBG GitHub page](https://github.com/sgl-project/rbg). You will get a forked repository that you fully control.

2. **Clone your forked repository**  
    Clone the forked repository to your local machine.

    ```shell
    git clone https://github.com/<your-username>/rbg.git
    ```

3. **Set upstream remote**  

    ```shell
    cd rbg
    git remote add upstream https://github.com/sgl-project/rbg.git
    # Safety guard: ensure push-remote is still disabled
    git remote set-url --push upstream no-pushing
    ```

4. **Sync your local code with upstream**  

    ```shell
    git fetch upstream
    git checkout main
    git rebase upstream/main
    ```

5. **Create a new branch for your work**  

    ```shell
    git checkout -b <new-branch>
    ```

### Update Generated Code

`Makefile` under project directory provides many tasks you may want to use including Test, Build, Debug, Deploy etc.

When your modification involves the CRD API definition, you need to update the generated code

```shell
# Generates WebhookConfiguration, CustomResourceDefinition objects.
$ make manifests
# Generates code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
$ make generate
```

### Build Binary

You can simply get a binary by running:

```shell
# Build controller binary
$ make build
```

```shell
# If you want to build rbg cli
$ make build-cli
```

By default, the binary would be put under `<rbg-path>/bin/`, and the controller default executable file name is manager.

### Build Image

Before running RBG Controller, you need to push the built image to an accessible image registry or just use the default image.

1. Build image

    ```shell
    # Set name for image of controller
    $ export IMG=<your-registry>/<your-namespace>/<img-name>
    # Set tag name
    $ export TAG=<img-tag>

    # Build controller image, the complete image name is ${IMG}:${TAG}
    $ make docker-build
    ```

2. Login to a image registry

    Make sure you've login to a docker image registry that you'd like to push your image to:

    ```shell
    docker login <your-registry> -u <username>
    ```

3. Push your image:

    ```shell
    # Push controller image
    $ make docker-push
    ```

### Run Your RBG on Kubernetes Cluster

In the following steps, we assume you have properly configured `KUBECONFIG` environment variable or set up `~/.kube/config`. See [Kubeconfig docs](https://kubernetes.io/docs/tasks/access-application-cluster/configure-access-multiple-clusters/) for more information.

1. Push your images to a image registry accessible to your Kubernetes cluster

    If your images are pushed to some private repositories, make sure your Kubernetes cluster hold credentials for accessing those repositories. You can add image pull credentials in [controller deploy](../../config/manager/manager.yaml)

    ```yaml
    spec:
      template:
        spec:
          imagePullSecrets:
            - name: dockerconfig-secret
    ```

2. Specify the custom controller image name to use

    ```shell
    # Set name for image of controller
    $ export IMG=<your-registry>/<your-namespace>/<img-name>
    # Set tag name
    $ export TAG=<img-tag>
    ```

3. Install CRDs

    ```shell
    make install
    ```

    Check CRD with:

    ```shell
    $ kubectl get crd | grep -e rolebasedgroup -e clusterengineruntimeprofiles
    clusterengineruntimeprofiles.workloads.x-k8s.io                  2025-09-01T09:22:57Z
    rolebasedgroups.workloads.x-k8s.io                               2025-09-01T09:22:58Z
    rolebasedgroupscalingadapters.workloads.x-k8s.io                 2025-09-01T09:22:58Z
    rolebasedgroupsets.workloads.x-k8s.io                            2025-09-01T09:22:58Z
    ```

4. Install your implementation

    ```shell
    make deploy
    ```

    Check rbg system with:

    ```shell
    $ kubectl get po -n rbgs-system
    NAME                                            READY   STATUS    RESTARTS   AGE
    rbgs-rbgs-controller-manager-5ccdb694f7-67cmv   1/1     Running   0          7h46m
    rbgs-rbgs-controller-manager-5ccdb694f7-6kx4d   1/1     Running   0          7h46m
    ```

5. Run samples to verify your implementation

    Here is a sample provided by us, you may want to rewrite it according to your implementation.

    ```shell
    kubectl apply -f examples/rbgs/rbgs-base.yaml
    ```

    Check sample pods:

    ```shell
    $ kubectl get po
    NAME                                      READY   STATUS    RESTARTS   AGE
    rbgs-test-jljp6-role-1-0                  1/1     Running   0          2m9s
    rbgs-test-jljp6-role-2-7b96bb6f8-2vmgd    1/1     Running   0          2m9s
    rbgs-test-jljp6-role-2-7b96bb6f8-bxtdw    1/1     Running   0          2m9s
    rbgs-test-xmgwc-role-1-0                  1/1     Running   0          2m9s
    rbgs-test-xmgwc-role-2-7c94f4b658-nq6jj   1/1     Running   0          2m9s
    rbgs-test-xmgwc-role-2-7c94f4b658-wrm72   1/1     Running   0          2m9s
    ```

6. Check logs to verify your implementation

    ```shell
    kubectl logs -n rbgs-system <controller_manager_name>
    ```

7. Clean up

    ```shell
    make undeploy
    ```

### Unit Testing

#### Basic Tests

Execute following command from project root to run basic unit tests:

```shell
make test
```

#### Integration Tests

Execute following command from project root to run integration tests:

```shell
make test-e2e
```

### Running RGB Controller Locally

The RGB controller supports local operation or debugging. Before running the controller locally, it is necessary to configure kubeconfig in advance in the local environment (configured through the `KUBECONFIG` environment variable or through the `$HOME/.kube/config` file) and be able to access a Kubernetes cluster normally.

1. Install CRDs

    ```shell
    make install
    ```

    Check CRD with:

    ```shell
    $ kubectl get crd | grep -e rolebasedgroup -e clusterengineruntimeprofiles
    clusterengineruntimeprofiles.workloads.x-k8s.io                  2025-09-01T09:22:57Z
    rolebasedgroups.workloads.x-k8s.io                               2025-09-01T09:22:58Z
    rolebasedgroupscalingadapters.workloads.x-k8s.io                 2025-09-01T09:22:58Z
    rolebasedgroupsets.workloads.x-k8s.io                            2025-09-01T09:22:58Z
    ```

2. Build binary:

    ```shell
    # Build controller binary
    $ make build
    ```

    By default, the binary would be put under `<rbg-path>/bin/`, and the default executable file name is manager.

3. Run Controller:

    ```shell
    # Open the development debugging mode, configure health-probe-bind-address
    $ ./bin/manager --development=true --health-probe-bind-address=:8082
    ```

### Debugging RGB Controller

The RBG controller component supports local operation or debugging. Before running the controller component locally, it is necessary to configure kubeconfig in advance in the local environment (configured through the `KUBECONFIG` environment variable or through the `$HOME/.kube/config` file) and be able to access a Kubernetes cluster normally.

#### Debugging with Local Command Line

Ensure that go help is installed in the environment, and refer to the [go installation manual](https://github.com/go-delve/delve/tree/master/Documentation/installation) for the specific installation process

```shell
dlv debug cmd/rbgs/main.go
```

#### Debugging with VSCode Locally

If VSCode is used as the development environment, the [Go plugin](https://marketplace.visualstudio.com/items?itemName=golang.go) of VSCode can be directly installed and conduct local debugging.

##### Debugging Controller Components

The Go code debugging task is defined in `./.vscode/launch.json` as follows:

```json
{
    "version": "0.2.0",
    "configurations": [
       {
            "name": "RBG Controller",
            "type": "go",
            "request": "launch",
            "mode": "debug",
            "program": "cmd/rbgs/main.go",
            "args": ["--development=true", "--health-probe-bind-address=:8082"],
            "env": {
                // "KUBECONFIG": "<path>/<to>/<kubeconfig>"
            }
        },
    ]
}
```

#### Remote Debugging

Please ensure that go help is correctly installed on both the local machine and component images.

On remote host:

```shell
dlv debug --headless --listen ":12345" --log --api-version=2 cmd/rbgs/main.go
```

This will cause the remote host's debugging program to listen to the specified port (e.g. 12345)

On local machine:

```shell
dlv connect "<remote-addr>:12345" --api-version=2
```

> Note: To debug remotely, make sure the specified port is not occupied and the firewall has been properly configured.
