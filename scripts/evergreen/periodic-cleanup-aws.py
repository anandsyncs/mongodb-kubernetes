import argparse
import re
from datetime import datetime, timedelta, timezone
from typing import List

import boto3

REPOSITORIES_NAMES = ["dev/mongodb-agent-ubi"]
REGISTRY_ID = "268558157000"
REGION = "us-east-1"
DEFAULT_AGE_THRESHOLD_DAYS = 1  # Number of days to consider as the age threshold
BOTO_MAX_PAGE_SIZE = 1000

ecr_client = boto3.client("ecr", region_name=REGION)


def describe_all_ecr_images(repository: str) -> List[dict]:
    """Retrieve all ECR images from the repository."""
    images = []

    # Boto3 can only return a maximum of 1000 images per request, we need a paginator to retrieve all images
    # from the repository
    paginator = ecr_client.get_paginator("describe_images")

    page_iterator = paginator.paginate(
        repositoryName=repository, registryId=REGISTRY_ID, PaginationConfig={"PageSize": BOTO_MAX_PAGE_SIZE}
    )

    for page in page_iterator:
        details = page.get("imageDetails", [])
        images.extend(details)

    return images


def filter_images_matching_tag(images: List[dict]) -> List[dict]:
    """Filter list for images containing the target pattern"""
    images_matching_tag = []
    for image_detail in images:
        if "imageTags" in image_detail:
            for tag in image_detail["imageTags"]:
                # The Evergreen patch id we use for building the test images tags uses an Object ID
                # https://www.mongodb.com/docs/v6.2/reference/bson-types/#std-label-objectid
                # It uses a timestamp, so it will always have the same prefix for a while (_6 in that case)
                # This must be changed before: July 2029
                # 70000000 -> decimal -> 1879048192 => Wednesday, July 18, 2029
                # Note that if the operator ever gets to major version 6, some tags can unintentionally match '_6'
                # It is an easy and relatively reliable way of identifying our test images tags
                if "_6" in tag or ".sig" in tag or contains_timestamped_tag(tag):
                    images_matching_tag.append({"imageTag": tag, "imagePushedAt": image_detail["imagePushedAt"]})
    return images_matching_tag


# match 107.0.0.8502-1-b20241125T000000Z-arm64
def contains_timestamped_tag(s: str) -> bool:
    if "b" in s and "T" in s and "Z" in s:
        pattern = r"b\d{8}T\d{6}Z"
        return bool(re.search(pattern, s))
    return False


def get_images_with_dates(repository: str) -> List[dict]:
    """Retrieve the list of patch images, corresponding to the regex, with push dates"""
    ecr_images = describe_all_ecr_images(repository)
    print(f"Found {len(ecr_images)} images in repository {repository}")
    images_matching_tag = filter_images_matching_tag(ecr_images)

    return images_matching_tag


def delete_image(repository: str, image_tag: str) -> None:
    ecr_client.batch_delete_image(repositoryName=repository, registryId=REGISTRY_ID, imageIds=[{"imageTag": image_tag}])
    print(f"Deleted image with tag: {image_tag}")


def delete_images(
    repository: str,
    images_with_dates: List[dict],
    age_threshold: int = DEFAULT_AGE_THRESHOLD_DAYS,
    dry_run: bool = False,
) -> None:
    # Get the current time in UTC
    current_time = datetime.now(timezone.utc)

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


def cleanup_repository(repository: str, age_threshold: int = DEFAULT_AGE_THRESHOLD_DAYS, dry_run: bool = False):
    print(f"Cleaning up images older than {DEFAULT_AGE_THRESHOLD_DAYS} day(s) from repository {repository}")
    print("Getting list of images...")
    images_with_dates = get_images_with_dates(repository)
    print(f"Images matching the pattern: {len(images_with_dates)}")

    # Sort the images by their push date (oldest first)
    images_with_dates.sort(key=lambda x: x["imagePushedAt"])

    delete_images(repository, images_with_dates, age_threshold, dry_run)
    print(f"Repository {repository} cleaned up")


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
