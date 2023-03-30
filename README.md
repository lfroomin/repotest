repotest is a tool used to execute all unit tests
within the monorepo workspace.

Command line options:
- showFail (display failed test details) default false
- useCache (use test cache) default true

repotest can be executed from anywhere within the repo
workspace. It will traverse from the current directory
towards the root until it locates the go.work file.