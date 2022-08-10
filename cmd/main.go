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
	ID      string
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

func SearchVarName(varAccessID string, varMap map[string]string) string {
	for {
		newVarAccessID, ok := varMap[varAccessID]
		if !ok {
			break
		}

		if newVarAccessID == "" {
			break
		}

		varAccessID = newVarAccessID
	}

	return varAccessID
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

func HasVarDataRace(varAccessInfo, sharedVarAccessInfo *VarAccessInfo) bool {
	varAccessFuncID := fmt.Sprintf("%s.%s", varAccessInfo.PkgID, varAccessInfo.FuncID)
	sharedVarAccessFuncID := fmt.Sprintf("%s.%s", sharedVarAccessInfo.PkgID, sharedVarAccessInfo.FuncID)

	if varAccessFuncID == sharedVarAccessFuncID {
		return false
	}

	isVarAccessFuncGR := IsGoRoutine(varAccessFuncID, goRoutineMap)
	isSharedVarAccessFuncGR := IsGoRoutine(sharedVarAccessFuncID, goRoutineMap)

	if !isVarAccessFuncGR && !isSharedVarAccessFuncGR {
		return false
	}

	if isVarAccessFuncGR {
		if varAccessFuncCreator, ok := goRoutineCreatorMap[varAccessFuncID]; ok {
			varAccessFuncCreatorFuncID := fmt.Sprintf("%s.%s", varAccessFuncCreator.PkgID, varAccessFuncCreator.FuncID)
			if varAccessFuncCreatorFuncID == sharedVarAccessFuncID {
				if sharedVarAccessInfo.Pos < varAccessFuncCreator.Pos {
					return false
				}
			}
		}
	}
	if isSharedVarAccessFuncGR {
		if sharedVarAccessFuncCreator, ok := goRoutineCreatorMap[sharedVarAccessFuncID]; ok {
			shareVarAccessFCreatorFID := fmt.Sprintf("%s.%s", sharedVarAccessFuncCreator.PkgID, sharedVarAccessFuncCreator.FuncID)
			if shareVarAccessFCreatorFID == varAccessFuncID {
				if varAccessInfo.Pos < sharedVarAccessFuncCreator.Pos {
					return false
				}
			}
		}
	}

	for lockID := range varAccessInfo.LockSet {
		if _, ok := sharedVarAccessInfo.LockSet[lockID]; ok {
			return false
		}
	}

	return true
}

func ReportPotentialDataRace(varAccessInfo, sharedVarAccess *VarAccessInfo) {
	fmt.Println("Potential data race:")
	fmt.Println("Var ID:")
	fmt.Println(varAccessInfo.ID)
	fmt.Println("Local Access Pos:")
	fmt.Printf("%s.%s.%d.%d\n", varAccessInfo.PkgID, varAccessInfo.FuncID, varAccessInfo.BlockID, varAccessInfo.Instr.Pos())
	fmt.Println("Local Access Instr:")
	fmt.Println(varAccessInfo.Instr.String())
	fmt.Println("Target Access Pos:")
	fmt.Printf("%s.%s.%d.%d\n", sharedVarAccess.PkgID, sharedVarAccess.FuncID, sharedVarAccess.BlockID, sharedVarAccess.Instr.Pos())
	fmt.Println("Target Access Instr:")
	fmt.Println(sharedVarAccess.Instr.String())
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
						ID:      varID,
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
								ID:      varID,
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
