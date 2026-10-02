param(
    [ValidateSet('Build', 'Deploy')][string]$Action = 'Build',
    [Parameter(Mandatory=$true)][string]$ProjectId,
    [Parameter(Mandatory=$true)][string]$Region,
    [string]$Service = 'gateforge',
    [string]$Repository = 'cloud-run-source-deploy',
    [string]$Gcloud = 'gcloud',
    [string]$BuildId
)
$ErrorActionPreference = 'Stop'
$taskRoot = Split-Path -Parent $PSScriptRoot
function Invoke-Cloud {
    $result = & $Gcloud @args
    if ($LASTEXITCODE -ne 0) { throw 'Google Cloud command failed.' }
    return $result
}
Push-Location $taskRoot
try {
    $revision = (& git rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $revision -notmatch '^[0-9a-f]{40}$') { throw 'A Git commit is required.' }
    if (& git status --porcelain) { throw 'Commit the reviewed changes before releasing.' }
    $imageRepository = "$Region-docker.pkg.dev/$ProjectId/$Repository/$Service"
    if ($Action -eq 'Build') {
        # Export only this commit. Local credentials and untracked files never enter the upload.
        $releaseDir = Join-Path $taskRoot ('.cache/releases/' + $revision + '-' + [guid]::NewGuid().ToString('N'))
        $sourceDir = Join-Path $releaseDir 'source'
        New-Item -ItemType Directory -Path $sourceDir -Force | Out-Null
        $archive = Join-Path $releaseDir 'source.tar'
        & git archive --format=tar "--output=$archive" $revision
        if ($LASTEXITCODE -ne 0) { throw 'Source export failed.' }
        & tar -xf $archive -C $sourceDir
        if ($LASTEXITCODE -ne 0) { throw 'Source extraction failed.' }
        $image = $imageRepository + ':' + $revision
        $build = Invoke-Cloud builds submit $sourceDir "--project=$ProjectId" "--region=$Region" `
            "--config=$sourceDir/infra/gcp/cloudbuild.yaml" "--substitutions=_IMAGE=$image,_REVISION=$revision" `
            "--service-account=projects/$ProjectId/serviceAccounts/gateforge-build@$ProjectId.iam.gserviceaccount.com" `
            "--gcs-source-staging-dir=gs://$ProjectId-build-source-eu/source" --async --quiet --format=json | ConvertFrom-Json
        [pscustomobject]@{Revision=$revision; BuildId=$build.id; Image=$image} | ConvertTo-Json
        return
    }
    if (!$BuildId) { throw 'Deploy requires the successful BuildId returned by Build.' }
    $build = Invoke-Cloud builds describe $BuildId "--project=$ProjectId" "--region=$Region" --format=json | ConvertFrom-Json
    if ($build.status -ne 'SUCCESS') { throw 'The release build and its tests must succeed before deployment.' }
    if ($build.substitutions._REVISION -ne $revision) { throw 'The build must match the checked-out commit.' }
    $builtImage = @($build.results.images | Where-Object { $_.name -eq ($imageRepository + ':' + $revision) })
    if ($builtImage.Count -ne 1 -or $builtImage[0].digest -notmatch '^sha256:[0-9a-f]{64}$') { throw 'A matching immutable image is required.' }
    $image = $imageRepository + '@' + $builtImage[0].digest
    $serviceState = Invoke-Cloud run services describe $Service "--project=$ProjectId" "--region=$Region" --format=json | ConvertFrom-Json
    $expectedContainers = @('gateway', 'users1', 'users2', 'orders', 'catalog')
    $actualContainers = @($serviceState.spec.template.spec.containers.name)
    if (Compare-Object $expectedContainers $actualContainers) { throw 'Unexpected container layout; review the service before updating.' }
    $recordDir = Join-Path $taskRoot '.local/cloudrun'
    New-Item -ItemType Directory -Path $recordDir -Force | Out-Null
    $record = [pscustomobject]@{
        Revision=$revision; BuildId=$BuildId; Image=$image
        PreviousRevision=$serviceState.status.latestReadyRevisionName
        PreviousTraffic=$serviceState.status.traffic
    }
    $record | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $recordDir "release-$revision.json")
    # Update images only; retain secret references, networking, IAM, limits and authentication.
    $arguments = @('run', 'services', 'update', $Service, "--project=$ProjectId", "--region=$Region",
        "--update-labels=gateforge-revision=$revision", '--async', '--quiet', '--format=value(status.latestCreatedRevisionName)')
    foreach ($container in $expectedContainers) { $arguments += @("--container=$container", "--image=$image") }
    $createdRevision = Invoke-Cloud @arguments
    [pscustomobject]@{Revision=$revision; Image=$image; CreatedRevision=$createdRevision; PreviousRevision=$record.PreviousRevision} | ConvertTo-Json
} finally { Pop-Location }
