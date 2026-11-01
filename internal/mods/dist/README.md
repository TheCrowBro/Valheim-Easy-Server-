The macOS release build puts libdoorstop.dylib here before building the app,
which carries it: UnityDoorstop (https://github.com/NeighTools/UnityDoorstop,
LGPL-2.1), built for macOS 11 and later. It loads BepInEx, and so the mods,
into the macOS server. Builds without it use the one in BepInExPack_Valheim,
which needs the newest macOS.
