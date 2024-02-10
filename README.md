```
#!/usr/bin/env bash

set -o pipefail

false | echo okkk

exit $?
```