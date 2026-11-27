@setlocal enableextensions
@cd /d "%~dp0"
SET PZ_CLASSPATH=java/;java/projectzomboid.jar
".\jre64\bin\java.exe" -Djava.awt.headless=true -Dzomboid.steam=1 -Dzomboid.znetlog=1 -XX:+UseZGC -XX:-CreateCoredumpOnCrash -XX:-OmitStackTraceInFastThrow -Xms16g -Xmx16g -Djava.library.path=natives/ -cp %PZ_CLASSPATH% zombie.network.GameServer -statistic 0 %1 %2
PAUSE

