# Google Compute Engine Custom Target Sample

This sample demonstrates how to use a Cloud Deploy custom target to deploy applications to Google Compute Engine (GCE).

## Overview

This sample includes the following components:

*   **`gce-deployer`**: A Go application that implements the custom target logic for deploying to GCE. It is containerized and registered as a custom target type in Cloud Deploy.
*   **`quickstart`**: A sample application that includes a `clouddeploy.yaml` file for a delivery pipeline, a `skaffold.yaml` file, and sample GCE manifests.
*   **`build_and_register.sh`**: A script that builds the `gce-deployer` container image and registers the custom target type with Cloud Deploy.

## Prerequisites

*   A Google Cloud project.
*   The `gcloud` CLI installed and configured.
*   Permissions to enable APIs, build container images, and manage Cloud Deploy resources.

## Setup

1.  **Set your project ID:**

    ```sh
    export PROJECT_ID="your-project-id"
gcloud config set project "${PROJECT_ID}"
    ```

2.  **Build and register the custom target type:**

    ```sh
    ./build_and_register.sh "${PROJECT_ID}"
    ```

    This script will build the `gce-deployer` container image and register it as a custom target type named `gce` in Cloud Deploy.

## Usage

1.  **Create the delivery pipeline and targets:**

    Navigate to the `quickstart` directory and run the following command:

    ```sh
    gcloud deploy apply --file=clouddeploy.yaml --region=us-central1 --project="${PROJECT_ID}"
    ```

2.  **Create a release:**

    ```sh
    gcloud deploy releases create "release-001" \
      --delivery-pipeline="gce-pipeline" \
      --region="us-central1" \
      --project="${PROJECT_ID}" \
      --images="gcr.io/google-samples/hello-app=gcr.io/google-samples/hello-app:1.0"
    ```

    This will create a new release and start the deployment to the `dev` target.

3.  **Promote the release to prod:**

    After the `dev` deployment succeeds, you can promote the release to the `prod` target, which will trigger a canary deployment.

    ```sh
    gcloud deploy releases promote "release-001" \
      --delivery-pipeline="gce-pipeline" \
      --region="us-central1" \
      --project="${PROJECT_ID}"
    ```

## Cleanup

To clean up the resources created by this sample, run the following commands:

```sh
gcloud deploy delivery-pipelines delete "gce-pipeline" --region="us-central1" --project="${PROJECT_ID}" --force
gcloud deploy targets delete "gce-dev" --region="us-central1" --project="${PROJECT_ID}" --force
gcloud deploy targets delete "gce-prod" --region="us-central1" --project="${PROJECT_ID}" --force
gcloud deploy custom-target-types delete "gce" --region="us-central1" --project="${PROJECT_ID}" --force
gcloud container images delete "gcr.io/${PROJECT_ID}/gce-deployer:render" --project="${PROJECT_ID}" --force-delete-tags --quiet
gcloud container images delete "gcr.io/${PROJECT_ID}/gce-deployer:deploy" --project="${PROJECT_ID}" --force-delete-tags --quiet
```