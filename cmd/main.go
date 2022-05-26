package main

import (
	"fmt"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
	"strings"
)

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

func main() {
	pkgCfg := &packages.Config{Mode: packages.LoadAllSyntax}

	pkgs, err := packages.Load(pkgCfg, "./testdata/sample.go")
	if err != nil {
		panic(err)
	}

	prog, _ := ssautil.AllPackages(pkgs, 0)
	prog.Build()

	ssaFuncMap := ssautil.AllFunctions(prog)

	type VarInfo struct {
		PkgID   string
		FuncID  string
		BlockID int
		Name    string
		Read    bool
		Write   bool
	}

	sharedVarMap := make(map[string][]VarInfo)

	globalVarMap := make(map[string]string)

	goRoutineMap := make(map[string]bool)

	for ssaFunc, _ := range ssaFuncMap {
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
			varPrefix := fmt.Sprintf("%s.%s.%d", pkgID, funcID, blockID)

			for _, instr := range block.Instrs {
				//Debug
				//fmt.Println(instr.String())
				//fmt.Println(reflect.ValueOf(instr).Type())
				//fmt.Println()

				if allocInstr, ok := instr.(*ssa.Alloc); ok {
					instrName := allocInstr.Name()
					varName := allocInstr.Comment

					globalVarMap[varPrefix+"."+varName] = ""
					globalVarMap[varPrefix+"."+instrName] = varPrefix + "." + varName
				}

				if makeClosureInstr, ok := instr.(*ssa.MakeClosure); ok {
					closureName := makeClosureInstr.Fn.Name()
					closureID := fmt.Sprintf("%s.%s.%d", pkgID, closureName, 0)

					for _, bind := range makeClosureInstr.Bindings {
						bindName := varPrefix + "." + bind.Name()
						bindVarName := SearchVarName(bindName, globalVarMap)
						bindVarNameParts := strings.Split(bindVarName, ".")
						rawBindVarName := bindVarNameParts[3]

						globalVarMap[closureID+"."+rawBindVarName] = bindVarName
					}
				}

				if goInstr, ok := instr.(*ssa.Go); ok {
					goRoutineMap[goInstr.Call.StaticCallee().String()] = true
				}
			}
		}
	}

	for ssaFunc, _ := range ssaFuncMap {
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
			varPrefix := fmt.Sprintf("%s.%s.%d", pkgID, funcID, blockID)

			for _, instr := range block.Instrs {
				//Debug
				//fmt.Println(instr.String())
				//fmt.Println(reflect.ValueOf(instr).Type())
				//fmt.Println()

				if storeInstr, ok := instr.(*ssa.Store); ok {
					varName := SearchVarName(varPrefix+"."+storeInstr.Addr.Name(), globalVarMap)

					scopeConflict := false

					if sharedVars, ok := sharedVarMap[varName]; ok {
						for _, sharedVar := range sharedVars {
							if sharedVar.PkgID != pkgID {
								scopeConflict = true
							}
							if sharedVar.FuncID != funcID {
								scopeConflict = true
							}
							//if sharedVar.BlockID != blockID {
							//	scopeConflict = true
							//}
							if scopeConflict {
								if !IsGoRoutine(sharedVar.PkgID+"."+sharedVar.FuncID, goRoutineMap) && !IsGoRoutine(pkgID+"."+funcID, goRoutineMap) {
									scopeConflict = false
								}
							}
							if scopeConflict {
								break
							}
						}
					}

					if scopeConflict {
						fmt.Println("Potential data race")
						fmt.Println(pkgID)
						fmt.Println(funcID)
						fmt.Println(varName)
						fmt.Println(instr.String())
						fmt.Println()
					}

					sharedVarMap[varName] = append(sharedVarMap[varName], VarInfo{
						PkgID:   pkgID,
						FuncID:  funcID,
						BlockID: blockID,
						Name:    varName,
						Write:   true,
					})
				}

				if unOpInstr, ok := instr.(*ssa.UnOp); ok {
					if unOpInstr.Op.IsOperator() {
						if unOpInstr.Op.String() == "*" {
							varName := SearchVarName(varPrefix+"."+unOpInstr.X.Name(), globalVarMap)

							scopeConflict := false

							if sharedVars, ok := sharedVarMap[varName]; ok {
								for _, sharedVar := range sharedVars {
									if sharedVar.PkgID != pkgID {
										scopeConflict = true
									}
									if sharedVar.FuncID != funcID {
										scopeConflict = true
									}
									//if sharedVar.BlockID != blockID {
									//	scopeConflict = true
									//}
									if scopeConflict {
										if sharedVar.Read {
											scopeConflict = false
										}
									}
									if scopeConflict {
										if !IsGoRoutine(sharedVar.PkgID+"."+sharedVar.FuncID, goRoutineMap) && !IsGoRoutine(pkgID+"."+funcID, goRoutineMap) {
											scopeConflict = false
										}
									}
									if scopeConflict {
										break
									}
								}
							}

							if scopeConflict {
								fmt.Println("Potential data race")
								fmt.Println(pkgID)
								fmt.Println(funcID)
								fmt.Println(varName)
								fmt.Println(instr.String())
								fmt.Println()
							}

							sharedVarMap[varName] = append(sharedVarMap[varName], VarInfo{
								PkgID:   pkgID,
								FuncID:  funcID,
								BlockID: blockID,
								Name:    varName,
								Read:    true,
							})
						}
					}
				}
			}
		}
	}

	//fmt.Println(globalVarMap)
	//fmt.Println()
	//fmt.Println(sharedVarMap)
	//fmt.Println()
	//fmt.Println(goRoutineMap)
}
