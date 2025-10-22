# Activator Chart

A chart to deploy the activator deployment and service per HTTPRoute.

## Install

To install an activator named `<route.name>-activator`, you can run the following command:

```txt
$ helm install activator ./charts/activator \
    --set route.name=http-route-name \
    --set inferencePool.name=inference-pool-name

```

## Uninstall

Run the following command to uninstall the chart:

```txt
$ helm uninstall activator
```

## Configuration

The following table list the configurable parameters of the chart.

| **Parameter Name**                          | **Description**                                                                                    |
|---------------------------------------------|----------------------------------------------------------------------------------------------------|
| `activator.suffix`                         | Suffix to append to the name of the activator deployment and service. Defaults to `-activator`.    |
| `activator.port`                            | Port serving ext_proc. Defaults to `9004`.  |
| `activator.healthCheckPort`                 | Port for health checks. Defaults to `9005`. |
| `activator.image.name`                      | Name of the container image used. |
| `activator.image.registry`                  | Registry URL and namespace where the image is hosted. |
| `activator.image.tag`              | Image tag. |
| `activator.image.pullPolicy`       | Image pull policy for the container. Possible values: `Always`, `IfNotPresent`, or `Never`. Defaults to `Always`. |
| `inferenceGateway.port`            | The port of the Gateway. Defaults to `80`.    |
| `inferencePool.name`               | The name of the InferencePool to target.  |
| `inferencePool.apiVersion`         | The API version of the InferencePool. Defaults to `inference.networking.x-k8s.io`.  |
| `route.name`                       | The name of the HTTPRoute to attach the activator to.  |

## Notes

This chart should only be deployed once per HTTPRoute.
