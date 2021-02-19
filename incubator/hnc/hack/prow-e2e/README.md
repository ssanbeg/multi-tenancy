This directory contains the files required to run HNC e2e tests on Prow using
Kind.

Files:
* Makefile: contains `build`, `run` and `clean` targets. `run` includes `build`.
  * Also 'push', which requires that you have permission to push to
    gcr.io/k8s-staging-multitenancy.
* Dockerfile: creates the image that contains Kind and everything else required
  to run the tests.
* run-e2e-tests.sh: included in the Docker image; pulls the repo, creates a Kind
  cluster, deploys HNC and runs the tests.

The Dockerfile depends on a specific version of the KRTE base image and KIND
(see comments in that file for details). You should update those every six
months or so, but probably nothing too bad will happen if you don't.

To run the tests in the container (i.e., debug the container), type `make run`,
followed by `make clean` if it doesn't finish successfully.
