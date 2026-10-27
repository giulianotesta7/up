Output docs/images/demo.gif
Set Shell bash
Set FontFamily "JetBrains Mono"
Set FontSize 16
Set Width 1200
Set Height 640
Set Padding 16
Set Framerate 20
Set Theme {"name":"up demo","black":"#0d1117","red":"#f85149","green":"#3fb950","yellow":"#d29922","blue":"#58a6ff","magenta":"#bc8cff","cyan":"#39c5cf","white":"#c9d1d9","brightBlack":"#8b949e","brightRed":"#ff7b72","brightGreen":"#56d364","brightYellow":"#e3b341","brightBlue":"#79c0ff","brightMagenta":"#d2a8ff","brightCyan":"#56d4dd","brightWhite":"#f0f6fc","background":"#0d1117","foreground":"#c9d1d9","selectionBackground":"#30363d","cursor":"#c9d1d9"}
Set TypingSpeed 50ms

Hide
# Open the demo database by a relative path so the footer shows "up.db", not a scratch path.
Type 'cd "$(dirname "${UP_DB:?record.sh must supply a demo database}")"; unset UP_DB NO_COLOR; up tui --db up.db'
Enter
Sleep 1s
Show
# Stay on the list long enough to see the next 10-second heartbeat sweep.
Sleep 10.5s
# Pause between keys so every view stays on screen long enough to read.
Down 3
Sleep 600ms
Enter
Sleep 4s
Escape
Sleep 1.2s
Up
Sleep 600ms
Enter
Sleep 3s
Escape
Sleep 1s
Hide
Type "q"
Sleep 500ms
