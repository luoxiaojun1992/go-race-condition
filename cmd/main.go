package main

import (
	"fmt"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
	"strings"
)

type VarAccessInfo struct {
	PkgID   string
	FuncID  string
	BlockID int
	Pos     int
	Name    string
	Read    bool
	Write   bool
	LockSet map[string]bool
	Instr   ssa.Instruction
}

type GoRoutineCreator struct {
	PkgID   string
	FuncID  string
	BlockID int
	Pos     int
}

var sharedVarAccessMap = make(map[string][]*VarAccessInfo)
var globalVarMap = make(map[string]string)

var goRoutineMap = make(map[string]bool)
var goRoutineCreatorMap = make(map[string]*GoRoutineCreator)

func SearchVarName(instrName string, varMap map[string]string) string {
	for {
		newInstrName, ok := varMap[instrName]
		if !ok {
			break
		}

		if newInstrName == "" {
			break
		}

		instrName = newInstrName
	}

	return instrName
}

func IsGoRoutine(fullFuncID string, goRoutineMap map[string]bool) bool {
	_, ok := goRoutineMap[fullFuncID]
	return ok
}

func CopyLockSet(lockSet map[string]bool) map[string]bool {
	lockSetCopy := make(map[string]bool)
	for lockID := range lockSet {
		lockSetCopy[lockID] = true
	}
	return lockSetCopy
}

func HasVarDataRace(varInfo, sharedVarInfo *VarAccessInfo) bool {
	varFuncID := fmt.Sprintf("%s.%s", varInfo.PkgID, varInfo.FuncID)
	sharedVarFuncID := fmt.Sprintf("%s.%s", sharedVarInfo.PkgID, sharedVarInfo.FuncID)

	if varFuncID == sharedVarFuncID {
		return false
	}

	if !IsGoRoutine(varFuncID, goRoutineMap) && !IsGoRoutine(sharedVarFuncID, goRoutineMap) {
		return false
	}

	for lockID := range varInfo.LockSet {
		if _, ok := sharedVarInfo.LockSet[lockID]; ok {
			return false
		}
	}

	return true
}

func ReportPotentialDataRace(varInfo, sharedVar *VarAccessInfo) {
	fmt.Println("Potential data race:")
	fmt.Println("Var Name:")
	fmt.Println(varInfo.Name)
	fmt.Println("Local Pos:")
	fmt.Printf("%s.%s.%d.%d\n", varInfo.PkgID, varInfo.FuncID, varInfo.BlockID, varInfo.Instr.Pos())
	fmt.Println("Local Instr:")
	fmt.Println(varInfo.Instr.String())
	fmt.Println("Target Pos:")
	fmt.Printf("%s.%s.%d.%d\n", sharedVar.PkgID, sharedVar.FuncID, sharedVar.BlockID, sharedVar.Instr.Pos())
	fmt.Println("Target Instr:")
	fmt.Println(sharedVar.Instr.String())
	fmt.Println()
}

func main() {
	pkgCfg := &packages.Config{Mode: packages.LoadAllSyntax}

	pkgs, err := packages.Load(pkgCfg, "./testdata/sample.go")
	if err != nil {
		panic(err)
	}

	prog, _ := ssautil.AllPackages(pkgs, 0)
	prog.Build()

	ssaFuncMap := ssautil.AllFunctions(prog)

	for ssaFunc := range ssaFuncMap {
		funcID := ssaFunc.Name()
		pkgID := ""

		ssaFuncPkg := ssaFunc.Package()
		if ssaFuncPkg != nil {
			pkgID = ssaFuncPkg.Pkg.Path()
		}

		if pkgID != "command-line-arguments" {
			continue
		}

		for _, block := range ssaFunc.Blocks {
			blockID := block.Index
			idPrefix := fmt.Sprintf("%s.%s.%d", pkgID, funcID, blockID)

			for _, instr := range block.Instrs {
				if allocInstr, ok := instr.(*ssa.Alloc); ok {
					instrName := allocInstr.Name()
					varName := allocInstr.Comment

					globalVarMap[idPrefix+"."+varName] = ""
					globalVarMap[idPrefix+"."+instrName] = idPrefix + "." + varName
				}

				if makeClosureInstr, ok := instr.(*ssa.MakeClosure); ok {
					closureName := makeClosureInstr.Fn.Name()
					closureID := fmt.Sprintf("%s.%s.%d", pkgID, closureName, 0)

					for _, bind := range makeClosureInstr.Bindings {
						bindVarAccessID := idPrefix + "." + bind.Name()
						bindVarID := SearchVarName(bindVarAccessID, globalVarMap)
						bindVarIDParts := strings.Split(bindVarID, ".")
						bindVarName := bindVarIDParts[3]

						globalVarMap[closureID+"."+bindVarName] = bindVarID
					}
				}

				if goInstr, ok := instr.(*ssa.Go); ok {
					goRoutineID := goInstr.Call.StaticCallee().String()
					goRoutineMap[goRoutineID] = true
					goRoutineCreatorMap[goInstr.Call.StaticCallee().String()] = &GoRoutineCreator{
						PkgID:   pkgID,
						FuncID:  funcID,
						BlockID: blockID,
						Pos:     int(goInstr.Pos()),
					}
				}
			}
		}
	}

	for ssaFunc := range ssaFuncMap {
		lockSet := make(map[string]bool)

		funcID := ssaFunc.Name()
		pkgID := ""

		ssaFuncPkg := ssaFunc.Package()
		if ssaFuncPkg != nil {
			pkgID = ssaFuncPkg.Pkg.Path()
		}

		if funcID == "init" {
			continue
		}

		if pkgID != "command-line-arguments" {
			continue
		}

		if funcID != "main" && funcID != "main$1" {
			continue
		}

		for _, block := range ssaFunc.Blocks {
			blockID := block.Index
			idPrefix := fmt.Sprintf("%s.%s.%d", pkgID, funcID, blockID)

			for _, instr := range block.Instrs {
				if callInstr, ok := instr.(*ssa.Call); ok {
					recv := callInstr.Call.StaticCallee().Signature.Recv()
					if recv != nil {
						if recv.Type().String() == "*sync.Mutex" {
							localLockID := fmt.Sprintf("%s.%s.%d.%s", pkgID, funcID, blockID, callInstr.Call.Args[0].Name())
							lockID := SearchVarName(localLockID, globalVarMap)

							callee := callInstr.Call.StaticCallee().Name()
							if callee == "Lock" {
								lockSet[lockID] = true
							} else if callee == "Unlock" {
								if _, ok := lockSet[lockID]; ok {
									delete(lockSet, lockID)
								}
							}
						}
					}
				}

				if storeInstr, ok := instr.(*ssa.Store); ok {
					varID := SearchVarName(idPrefix+"."+storeInstr.Addr.Name(), globalVarMap)

					varAccessInfo := &VarAccessInfo{
						PkgID:   pkgID,
						FuncID:  funcID,
						BlockID: blockID,
						Pos:     int(storeInstr.Pos()),
						Name:    varID,
						Write:   true,
						LockSet: CopyLockSet(lockSet),
						Instr:   instr,
					}

					if sharedVarAccessList, ok := sharedVarAccessMap[varID]; ok {
						for _, sharedVarAccess := range sharedVarAccessList {
							if HasVarDataRace(varAccessInfo, sharedVarAccess) {
								ReportPotentialDataRace(varAccessInfo, sharedVarAccess)
							}
						}
					}

					sharedVarAccessMap[varID] = append(sharedVarAccessMap[varID], varAccessInfo)
				}

				if unOpInstr, ok := instr.(*ssa.UnOp); ok {
					if unOpInstr.Op.IsOperator() {
						if unOpInstr.Op.String() == "*" {
							varID := SearchVarName(idPrefix+"."+unOpInstr.X.Name(), globalVarMap)

							varAccessInfo := &VarAccessInfo{
								PkgID:   pkgID,
								FuncID:  funcID,
								BlockID: blockID,
								Pos:     int(unOpInstr.Pos()),
								Name:    varID,
								Write:   true,
								LockSet: CopyLockSet(lockSet),
								Instr:   instr,
							}

							if sharedVarAccessList, ok := sharedVarAccessMap[varID]; ok {
								for _, sharedVarAccess := range sharedVarAccessList {
									if HasVarDataRace(varAccessInfo, sharedVarAccess) {
										ReportPotentialDataRace(varAccessInfo, sharedVarAccess)
									}
								}
							}

							sharedVarAccessMap[varID] = append(sharedVarAccessMap[varID], varAccessInfo)
						}
					}
				}
			}
		}
	}
}
