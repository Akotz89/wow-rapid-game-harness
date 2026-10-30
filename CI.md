# Public CI scope

Public CI checks Python syntax and lints the bootstrap shell script. It does not
compile the native harness, run its project-dependent tests, access a realm, or
prove gameplay. Native builds and tests remain subject to the project's WSL
build-host guard and require the project inputs documented in README.md.
