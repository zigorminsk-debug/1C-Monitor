# Запускает программу, ждёт главное окно, делает снимок экрана и завершает её.
# С ключом -Report ничего не требует: только сообщает, что получилось (запуск без OpenGL 2.1).
param([string]$Exe, [string]$Shot, [switch]$Report, [int]$Seconds = 25)

$ErrorActionPreference = 'Stop'

# Главное окно после первого измерения называется «<имя сервера> — 1C Server Monitor»;
# просто «1C Server Monitor» — это окно до первого измерения или сообщение об ошибке.
function Test-MainWindow([string]$Title) { return $Title.EndsWith('1C Server Monitor') -and $Title.Length -gt '1C Server Monitor'.Length }
Add-Type -AssemblyName System.Windows.Forms, System.Drawing

$data = Join-Path $env:LOCALAPPDATA '1CServerMonitor'
Remove-Item (Join-Path $data 'interface.log'), (Join-Path $data 'crash.log') -ErrorAction SilentlyContinue
$process = Start-Process -FilePath $Exe -PassThru
$title = ''
for ($i = 0; $i -lt $Seconds; $i++) {
    Start-Sleep -Seconds 1
    $process.Refresh()
    if ($process.HasExited) { break }
    $title = $process.MainWindowTitle
    if ((Test-MainWindow $title) -and $i -ge 8) { break }
}
$alive = -not $process.HasExited
$facts = "процесс работает: $alive; заголовок окна: «$title»"
Write-Host $facts

New-Item -ItemType Directory -Force (Split-Path $Shot) | Out-Null
$bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
$bitmap = New-Object System.Drawing.Bitmap $bounds.Width, $bounds.Height
$dark = 0; $samples = 0
try {
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $graphics.CopyFromScreen($bounds.Location, [System.Drawing.Point]::Empty, $bounds.Size)
    $bitmap.Save((Join-Path (Get-Location) $Shot))
    # Доля точек экрана в цветах тёмной темы программы: признак того, что окно действительно нарисовано.
    for ($x = 20; $x -lt $bounds.Width; $x += 40) {
        for ($y = 20; $y -lt $bounds.Height; $y += 40) {
            $c = $bitmap.GetPixel($x, $y); $samples++
            if ($c.R -lt 0x40 -and $c.G -lt 0x50 -and $c.B -lt 0x70 -and $c.B -gt $c.R) { $dark++ }
        }
    }
    $facts += "; экран $($bounds.Width)x$($bounds.Height), точек в цветах программы: $dark из $samples"
    Write-Host "экран $($bounds.Width)x$($bounds.Height); точек в цветах программы: $dark из $samples"
} catch {
    Write-Host "снимок экрана не получен: $_"
}

if ($alive) { Stop-Process -Id $process.Id -Force }
foreach ($name in 'interface.log', 'crash.log') {
    $file = Join-Path $data $name
    if (Test-Path $file) { Write-Host "--- $name"; Get-Content $file -Encoding UTF8 | Select-Object -Last 60 }
}
$log = ''
if (Test-Path (Join-Path $data 'interface.log')) { $log = Get-Content (Join-Path $data 'interface.log') -Raw -Encoding UTF8 }
# Сведения дублируются в аннотацию запуска: журнал шага доступен не всем инструментам.
$tail = (($log -split "`n" | Where-Object { $_.Trim() } | Select-Object -Last 12) -join '%0A') -replace "`r", ''
if ($tail.Length -gt 1500) { $tail = $tail.Substring($tail.Length - 1500) }
Write-Host "::notice title=$(Split-Path $Shot -Leaf)::$facts%0A$tail"
$noOpenGL = $log -match 'window creation error|failed to initialise GLFW'
if ($Report) {
    if ($noOpenGL) { Write-Host 'Итог: системный OpenGL не подходит, программа показала сообщение об ошибке (ожидаемо без видеодрайвера).' }
    elseif (Test-MainWindow $title) { Write-Host 'Итог: окно открылось на системном OpenGL.' }
    else { Write-Host 'Итог: неопределённый результат.' }
    exit 0
}
if (-not $alive) { throw 'программа завершилась' }
if ($noOpenGL) { throw 'окно не создано: OpenGL недоступен' }
if (-not (Test-MainWindow $title)) { throw "главное окно не появилось, заголовок: «$title»" }
if ($samples -gt 0 -and $dark * 2 -lt $samples) { throw "окно не нарисовано: точек в цветах программы $dark из $samples" }
Write-Host 'Итог: главное окно открылось и нарисовано.'
