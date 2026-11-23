syspatch; echo "syspatch rc=$?"
echo "--- installed"; syspatch -l
echo "--- pending"; syspatch -c
