# Helm Chart: lut-puller

This chart deploys the Microsoft Graph LUT puller as a Kubernetes `CronJob`. It fetches data via the container image (built from this repo) on a schedule, producing LUT text files.

## Configuration Options

| Key | Description | Default |
| --- | ----------- | ------- |
| `image.repository` | Docker image repository | `ghcr.io/lavlabinfrastructure/lut-puller` |
| `image.tag` | Image tag | `latest` |
| `image.pullPolicy` | Kubernetes image pull policy | `IfNotPresent` |
| `schedule` | Cron expression for job execution | `0 * * * *` |
| `config.existingSecret` | Name of an existing Secret containing YAML config files | `""` |
| `config.inlineConfigs` | Array of inline YAML configs to create as a Secret | `[]` |
| `configMountPath` | Mount path where configs are loaded | `/config` |
| `output.enabled` | Enable mounting an existing PVC | `true` |
| `output.existingClaim` | Name of PVC to mount for output | `""` |
| `output.mountPath` | Path to mount output PVC | `/data` |
| `extraEnv` | Additional environment variables | `[]` |
| `successfulJobsHistoryLimit` | Number of successful jobs to keep | `1` |
| `failedJobsHistoryLimit` | Number of failed jobs to keep | `1` |
| `backoffLimit` | Number of retries before job is marked failed | `1` |
| `startingDeadlineSeconds` | Optional deadline in seconds for starting the job | `null` |
| `concurrencyPolicy` | CronJob concurrency policy | `Forbid` |
| `pod.*` | Pod-level overrides (annotations, labels, etc.) | `{}` |

## Usage

### Install with inline configuration

```sh
helm install my-lut charts/lut-puller \
  --set image.repository=ghcr.io/your-org/lut-puller \
  --set image.tag=1.0.0 \
  --set output.existingClaim=lut-output \
  --set config.inlineConfigs[0].name=config.yaml \
  --set-file config.inlineConfigs[0].data=example.yaml
```

### Install using existing secret and PVC

```sh
helm install my-lut charts/lut-puller \
  --set image.repository=ghcr.io/your-org/lut-puller \
  --set image.tag=1.0.0 \
  --set config.existingSecret=lut-config \
  --set output.existingClaim=lut-output
```

> **Important:** The chart enforces that `output.mountPath` cannot equal `configMountPath` to avoid overwriting configuration files.
