param(
    [Parameter(Mandatory=$true)][ValidatePattern('^[a-z][a-z0-9-]{4,28}[a-z0-9]$')][string]$ProjectId,
    [Parameter(Mandatory=$true)][ValidatePattern('^[a-z]+-[a-z]+[0-9]+-[a-z]$')][string]$Zone,
    [switch]$Apply,
    [switch]$AcceptCloudCosts
)
$ErrorActionPreference = 'Stop'
if ($Apply -and -not $AcceptCloudCosts) { throw 'Review the plan and estimated costs before using -Apply -AcceptCloudCosts.' }
$taskRegion = $Zone.Substring(0, $Zone.Length - 2)
$taskStartup = Join-Path $PSScriptRoot 'startup.sh'
$taskCommands = @(
    @('services','enable','compute.googleapis.com','iap.googleapis.com',"--project=$ProjectId"),
    @('compute','networks','create','gateforge-net','--subnet-mode=custom',"--project=$ProjectId"),
    @('compute','networks','subnets','create','gateforge-subnet','--network=gateforge-net','--range=10.42.0.0/24',"--region=$taskRegion",'--enable-private-ip-google-access',"--project=$ProjectId"),
    @('compute','addresses','create','gateforge-ip',"--region=$taskRegion", "--project=$ProjectId"),
    @('compute','firewall-rules','create','gateforge-https','--network=gateforge-net','--allow=tcp:443','--source-ranges=0.0.0.0/0','--target-tags=gateforge',"--project=$ProjectId"),
    @('compute','firewall-rules','create','gateforge-iap-ssh','--network=gateforge-net','--allow=tcp:22','--source-ranges=35.235.240.0/20','--target-tags=gateforge',"--project=$ProjectId"),
    @('compute','instances','create','gateforge',"--zone=$Zone",'--machine-type=e2-medium','--subnet=gateforge-subnet','--address=gateforge-ip','--tags=gateforge','--image-family=debian-12','--image-project=debian-cloud','--boot-disk-size=50GB','--boot-disk-type=pd-balanced','--no-boot-disk-auto-delete','--deletion-protection','--no-service-account','--no-scopes','--metadata=enable-oslogin=TRUE',"--metadata-from-file=startup-script=$taskStartup",'--labels=app=gateforge',"--project=$ProjectId")
)
foreach ($taskArgs in $taskCommands) {
    Write-Output ('gcloud ' + ($taskArgs -join ' '))
    if ($Apply) { & gcloud @taskArgs; if ($LASTEXITCODE -ne 0) { throw 'Provisioning stopped. Inspect created resources before retrying; existing names are not overwritten.' } }
}
if (-not $Apply) { Write-Output 'Plan only. No resources created. Pricing and billing alerts are not a hard spending cap.' }
