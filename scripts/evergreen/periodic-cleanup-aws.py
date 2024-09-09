import argparse
from datetime import datetime, timedelta, timezone
from typing import List

import boto3

REPOSITORIES_NAMES = ["dev/mongodb-agent-ubi"]
REGISTRY_ID = "268558157000"
REGION = "us-east-1"
DEFAULT_AGE_THRESHOLD_DAYS = 1  # Number of days to consider as the age threshold
MAX_BOTO_IMAGES = 1000

ecr_client = boto3.client("ecr", region_name=REGION)


def get_images_with_dates(repository: str) -> List[dict]:
    """Retrieve the list of patch images, corresponding to the regex with push dates"""
    images_with_dates = []
    response = ecr_client.describe_images(
        repositoryName=repository,
        registryId=REGISTRY_ID,
        maxResults=MAX_BOTO_IMAGES,
    )

    for image_detail in response["imageDetails"]:
        if "imageTags" in image_detail:
            for tag in image_detail["imageTags"]:
                # The Evergreen patch id we use for building the test images tags uses an Object ID
                # https://www.mongodb.com/docs/v6.2/reference/bson-types/#std-label-objectid
                # It uses a timestamp, so it will always have the same prefix for a while (_6 in that case)
                # This must be changed before: July 2029
                # 70000000 -> decimal -> 1879048192 => Wednesday, July 18, 2029
                # Note that if the operator ever gets to major version 6, some tags can unintentionally match '_6'
                # It is an easy and relatively reliable way of identifying our test images tags
                if "_6" in tag:
                    images_with_dates.append({"imageTag": tag, "imagePushedAt": image_detail["imagePushedAt"]})

    return images_with_dates


def delete_image(repository: str, image_tag: str) -> None:
    ecr_client.batch_delete_image(repositoryName=repository, registryId=REGISTRY_ID, imageIds=[{"imageTag": image_tag}])
    print(f"Deleted image with tag: {image_tag}")


def cleanup_repository(repository: str, age_threshold: int = DEFAULT_AGE_THRESHOLD_DAYS, dry_run: bool = False):
    print(f"Cleaning up images older than {DEFAULT_AGE_THRESHOLD_DAYS} day(s) from repository {repository}")
    print(f"Due to boto3 limitations, only {MAX_BOTO_IMAGES} images are processed")
    print("Getting list of images...")
    images_with_dates = get_images_with_dates(repository)
    print(f"Images matching the pattern: {len(images_with_dates)}")

    # Get the current time in UTC
    current_time = datetime.now(timezone.utc)

    # Sort the images by their push date (oldest first)
    images_with_dates.sort(key=lambda x: x["imagePushedAt"])

    # Process the images, deleting those older than the threshold
    delete_count = 0
    age_threshold_timedelta = timedelta(days=age_threshold)
    for image in images_with_dates:
        tag = image["imageTag"]
        push_date = image["imagePushedAt"]
        image_age = current_time - push_date

        log_message_base = f"Image {tag}, was pushed at {push_date.isoformat()}"
        delete_message = "should be cleaned up" if dry_run else "deleting..."
        if image_age > age_threshold_timedelta:
            print(f"{log_message_base}, older than {age_threshold} day(s), {delete_message}")
            if not dry_run:
                delete_image(repository, tag)
            delete_count += 1
        else:
            print(f"{log_message_base}, not older than {age_threshold} day(s)")
    deleted_message = "need to be cleaned up" if dry_run else "deleted"
    print(f"{delete_count} images {deleted_message}")


def main():
    parser = argparse.ArgumentParser(description="Process and delete old ECR images.")
    parser.add_argument(
        "--age-threshold",
        type=int,
        default=DEFAULT_AGE_THRESHOLD_DAYS,
        help="Age threshold in days for deleting images (default: 1 day)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="If specified, only display what would be deleted without actually deleting.",
    )
    args = parser.parse_args()

    if args.dry_run:
        print("Dry run - not deleting images")

    for repository in REPOSITORIES_NAMES:
        cleanup_repository(repository, age_threshold=args.age_threshold, dry_run=args.dry_run)


if __name__ == "__main__":
    main()
