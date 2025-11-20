$dir = ".\demo\run-$(Get-Date -Format 'HHmmss')"
$exe = "$dir\logyard.exe"

go build -o $exe

Start-Process -FilePath "powershell.exe" -ArgumentList "-NoExit", "-Command", "Start-Sleep -Milliseconds 1000; &$exe -l -cl -id=server"

&$exe -demo 0 -l *>&1 | &$exe -c -l -cl -id=demo
