The release build puts the Discord waker for the JetKVM here, as
valheim-waker-armv7.gz (built from cmd/valheim-waker for linux/arm, GOARM=7),
before building the app, which carries it. Builds without it can't install
the waker.
