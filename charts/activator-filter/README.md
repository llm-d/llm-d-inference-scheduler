# Activator Chart

A chart to deploy the activator HTTP filter for an InferenceGateway and RBAC for all per route activator deployments.

## Install

To install an activator-filter named `activator-filter`, you can run the following command:

```txt
$ helm install activator-filter ./charts/activator-filter
```

## Uninstall

Run the following command to uninstall the chart:

```txt
$ helm uninstall activator-filter
```

## Configuration

The following table list the configurable parameters of the chart.

| **Parameter Name**                          | **Description**                                                                                    |
|---------------------------------------------|----------------------------------------------------------------------------------------------------|
| `name`                   | Name of the activator RBAC resources. Defaults to `activator`.  |

## Notes

This chart should only be deployed once.
