#!/bin/bash

# Bump version in main.go and create git tag (without pushing)

# Path to main.go
MAIN_FILE="main.go"

# Check if main.go exists
if [ ! -f "$MAIN_FILE" ]; then
    echo "Error: $MAIN_FILE not found!"
    exit 1
fi

# Read the current file content
FILE_CONTENT=$(cat "$MAIN_FILE")

# Extract current version - using a more targeted approach
CURRENT_VERSION=$(echo "$FILE_CONTENT" | grep -o 'version[ ]*=[ ]*"[0-9]\+\.[0-9]\+\.[0-9]\+"' | grep -o '[0-9]\+\.[0-9]\+\.[0-9]\+')

if [ -z "$CURRENT_VERSION" ]; then
    echo "Error: Could not extract version from $MAIN_FILE"
    exit 1
fi

echo "Current version: $CURRENT_VERSION"

# Split version into components
IFS='.' read -r MAJOR MINOR PATCH <<< "$CURRENT_VERSION"

# Increment patch version
NEW_PATCH=$((PATCH + 1))
NEW_VERSION="$MAJOR.$MINOR.$NEW_PATCH"

echo "New version: $NEW_VERSION"

# Update version in main.go - using a more direct approach with awk
awk -v old="version[ ]*=[ ]*\"$CURRENT_VERSION\"" -v new="version     = \"$NEW_VERSION\"" '{gsub(old, new); print}' "$MAIN_FILE" > temp.go && mv temp.go "$MAIN_FILE"

# Commit the change using Conventional Commits format
git add "$MAIN_FILE"
git commit -m "chore: bump version to $NEW_VERSION"

# Create git tag
git tag -a "v$NEW_VERSION" -m "Release v$NEW_VERSION"

echo "✅ Version bumped to $NEW_VERSION and tag created"
echo ""
echo "To push changes and trigger release:"
echo "  git push origin main && git push origin v$NEW_VERSION"
echo ""
echo "Or to push everything at once:"
echo "  git push origin main --follow-tags"
