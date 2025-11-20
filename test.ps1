if ($args[0] -eq "-d" -or $args[0] -eq "-dr") {
    Remove-Item ./test/* -Recurse
    if ($args[0] -eq "-d") {
        return
    }
}

$dir = "./test/run-$(Get-Date -Format 'HHmmss')"
$exe = "$dir/logyard.exe"
$data = "some test data for piping into stdin"

go build -o $exe

if ($args[0] -eq "-demo") {
    if ($args[2 -eq "-demoSleep"]) {
        &$exe -sl -demo $args[1] -id="demo" -demoSleep $args[3]
    } else {
        &$exe -sl -demo $args[1] -id="demo"
    }
    return
}

if ($args[0] -eq "-s") {
    Write-Output "# Server with standard logging"
    $_args = $args[1..($args.Count - 1)]
    Write-Output "# Extra arguments: $($_args)"
    $data | &$exe -l -id="capid-plain" @($_args)
} else {
    Write-Output "# Capture with standard logging"
    $data | &$exe -l -c -id="capid-plain"
    Write-Output "# Capture with rolling logging"
    $data | &$exe -cl -rl -c -id="capid-roll"
}