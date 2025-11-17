
gcloud config set project chris-test1-475920

gcloud compute health-checks create http gce-health-check \
    --port=8080 \
    --project chris-test1-475920

gcloud compute firewall-rules create gce-health-checks \
    --network=default \
    --action=ALLOW \
    --direction=INGRESS \
    --source-ranges=35.191.0.0/16,130.211.0.0/22 \
    --target-tags=health-check-tag \
    --rules=tcp:8080 \
    --project chris-test1-475920

    gcloud projects add-iam-policy-binding chris-test1-475920 \
      --member=serviceAccount:$(gcloud projects describe chris-test1-475920 \
      --format="value(projectNumber)")-compute@developer.gserviceaccount.com \
      --role="roles/clouddeploy.jobRunner"


      gcloud projects add-iam-policy-binding chris-test1-475920 \
        --member=serviceAccount:$(gcloud projects describe chris-test1-475920 \
        --format="value(projectNumber)")-compute@developer.gserviceaccount.com \
        --role="roles/container.developer"


        gcloud projects add-iam-policy-binding chris-test1-475920 \
          --member=serviceAccount:$(gcloud projects describe chris-test1-475920 \
          --format="value(projectNumber)")-compute@developer.gserviceaccount.com \
          --role="roles/compute.networkAdmin"


echo "management: automatic" > fleet-mesh-settings.yaml
gcloud container fleet mesh enable \
    --fleet-default-member-config fleet-mesh-settings.yaml \
    --project chris-test1-475920

          gcloud container clusters create-auto dev \
              --fleet-project chris-test1-475920 \
              --region=us-central1 \
              --autoprovisioning-network-tags=health-check-tag \
              --project chris-test1-475920

gcloud compute networks create lb-network --subnet-mode=custom

gcloud compute networks subnets create backend-subnet \
    --network=lb-network \
    --range=10.1.2.0/24 \
    --region=us-central1

gcloud compute networks subnets create proxy-only-subnet \
    --purpose=REGIONAL_MANAGED_PROXY \
    --role=ACTIVE \
    --region=us-central1 \
    --network=lb-network \
    --range=10.129.0.0/23

 gcloud compute addresses create l7-ilb-ip-address \
    --region=us-central1 \
    --subnet=backend-subnet


 gcloud compute instance-templates create l7-ilb-backend-template \
     --region=us-central1 \
     --network=lb-network \
     --subnet=backend-subnet \
    --tags=allow-ssh,load-balanced-backend,health-check-tag \
     --image-family=debian-12 \
     --image-project=debian-cloud \
     --metadata=startup-script='#! /bin/bash
     apt-get update
     apt-get install apache2 -y
     a2ensite default-ssl
     a2enmod ssl
     vm_hostname="$(curl -H "Metadata-Flavor:Google" \
     http://metadata.google.internal/computeMetadata/v1/instance/name)"
     echo "Page served from: $vm_hostname" | \
     tee /var/www/html/index.html
     systemctl restart apache2'

  gcloud compute instance-groups managed create l7-ilb-backend-example \
      --zone=us-central1-a \
      --size=1 \
      --template=l7-ilb-backend-template

  gcloud compute instance-groups managed describe l7-ilb-backend-example \
      --zone=us-central1-a

gcloud compute backend-services create l7-ilb-backend-service-global \
    --load-balancing-scheme=INTERNAL_MANAGED \
    --protocol=HTTP \
    --health-checks=gce-health-check \
     --region=us-central1

gcloud compute backend-services add-backend l7-ilb-backend-service \
    --balancing-mode=UTILIZATION \
    --instance-group=l7-ilb-backend-example \
    --instance-group-zone=us-central1-a \
    --region=us-central1

gcloud compute backend-services describe l7-ilb-backend-service \
    --region=us-central1

    gcloud compute backend-services describe dev-bs \
    --region=us-central1

     gcloud compute backend-services describe dev-bs-canary \
        --region=us-central1

    gcloud compute url-maps create l7-ilb-map \
        --default-service=l7-ilb-backend-service \
        --region=us-central1

    gcloud compute url-maps create l7-ilb-map-global \
        --default-service=l7-ilb-backend-service-global

gcloud compute url-maps describe l7-ilb-map \
         --region=us-central1

    {
      "kind": "compute#urlMap",
      "id": "3126998582711064301",
      "creationTimestamp": "2025-11-03T14:09:06.599-08:00",
      "name": "l7-ilb-map",
      "selfLink": "https://www.googleapis.com/compute/v1/projects/chris-test1-475920/regions/us-central1/urlMaps/l7-ilb-map",
      "defaultService": "https://www.googleapis.com/compute/v1/projects/chris-test1-475920/regions/us-central1/backendServices/l7-ilb-backend-service",
      "fingerprint": "hUm_bUonZHw=",
      "region": "https://www.googleapis.com/compute/v1/projects/chris-test1-475920/regions/us-central1"
    }


{
  "kind": "compute#urlMap",
  "id": "2860578608834187497",
  "creationTimestamp": "2025-11-03T14:34:46.713-08:00",
  "name": "l7-ilb-map",
  "selfLink": "https://www.googleapis.com/compute/v1/projects/chris-test1-475920/regions/us-central1/urlMaps/l7-ilb-map",
  "hostRules": [
    {
      "hosts": [
        "web.example.com"
      ],
      "pathMatcher": "path-matcher-1"
    }
  ],
  "pathMatchers": [
    {
      "name": "path-matcher-1",
      "defaultService": "https://www.googleapis.com/compute/v1/projects/chris-test1-475920/regions/us-central1/backendServices/bs1",
      "pathRules": [
        {
          "routeAction": {
            "weightedBackendServices": [
              {
                "backendService": "https://www.googleapis.com/compute/v1/projects/chris-test1-475920/regions/us-central1/backendServices/bs1",
                "weight": 50
              },
              {
                "backendService": "https://www.googleapis.com/compute/v1/projects/chris-test1-475920/regions/us-central1/backendServices/bs1-canary",
                "weight": 50
              }
            ]
          },
          "paths": [
            "/bs1/"
          ]
        },
        {
          "routeAction": {
            "weightedBackendServices": [
              {
                "backendService": "https://www.googleapis.com/compute/v1/projects/chris-test1-475920/regions/us-central1/backendServices/bs2",
                "weight": 50
              },
              {
                "backendService": "https://www.googleapis.com/compute/v1/projects/chris-test1-475920/regions/us-central1/backendServices/bs2-canary",
                "weight": 50
              }
            ]
          },
          "paths": [
            "/bs2/"
          ]
        }
      ]
    }
  ],
  "defaultService": "https://www.googleapis.com/compute/v1/projects/chris-test1-475920/regions/us-central1/backendServices/bs1",
  "fingerprint": "fOkFgdZZqIk=",
  "region": "https://www.googleapis.com/compute/v1/projects/chris-test1-475920/regions/us-central1"
}




./build_and_register.sh -p chris-test1-475920 -r us-central1

gcloud deploy apply  --region=us-central1 --file clouddeploy.yaml

gcloud deploy releases create a49 --delivery-pipeline=gce-pipeline --region=us-central1 --source=. --to-target=gce-prod


gcloud compute backend-services describe dev-bs \
    --region=us-central1


gcloud compute backend-services delete dev-bs \
    --region=us-central1

gcloud deploy targets describe gce-dev --region=us-central1
gcloud deploy targets rollback gce-dev --region=us-central1 --delivery-pipeline=gce-pipeline --release=a47 --starting-phase-id=c50