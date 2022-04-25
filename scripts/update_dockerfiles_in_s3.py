#!/usr/bin/env python3
#

"""
This script is used to push the dockerfiles from this repo (found in public/dockerfiles folder) to S3 bucket.
The main purpose of this sync is to keep the dockerfiles in S3 up to date with the latest security fixes for our periodic re-builds.

required environment vars (can also be added to /.operator-dev/om):
    - AWS_ACCESS_KEY_ID
    - AWS_SECRET_ACCESS_KEY

run the script:
    PYTHONPATH="<path to ops-manager-kubernetes repo>:<path to ops-manager-kubernetes repo>/docker/mongodb-enterprise-tests" python ./scripts/update_dockerfiles_in_s3.py
"""

import os
from scripts.add_supported_release import get_repo_root
from kubetester.awss3client import AwsS3Client


AWS_REGION = "eu-west-1"
S3_BUCKET = "enterprise-operator-dockerfiles"
S3_FOLDER = "dockerfiles"
S3_PUBLIC_READ = True

LOCAL_DOCKERFILE_LOCATION = "public/dockerfiles"

DOCKERFILE_NAME = "Dockerfile"

public_dir = os.path.join(get_repo_root(), LOCAL_DOCKERFILE_LOCATION)
client = AwsS3Client(AWS_REGION)

for root, _, files in os.walk(public_dir):
    for file_name in filter(lambda f: f == DOCKERFILE_NAME, files):
        file_path = os.path.join(root, file_name)
        object_name = file_path.replace(f"{public_dir}", S3_FOLDER, 1)
        client.upload_file(
            os.path.join(root, file_name), S3_BUCKET, object_name, S3_PUBLIC_READ
        )
        print(f" > {object_name}")

print("Done!")
