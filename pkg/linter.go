package pkg

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

type Linter struct {
	SharedVarAccessMap map[string][]*VarAccessInfo
	GlobalVarMap       map[string]string

	GoRoutineMap        map[string]bool
	GoRoutineCreatorMap map[string]*GoRoutineCreator

	SSAFuncMap map[*ssa.Function]bool

	FilePath string
}

func NewLinter(filePath string) (*Linter, error) {
	linter := &Linter{
		SharedVarAccessMap: make(map[string][]*VarAccessInfo),
		GlobalVarMap:       make(map[string]string),

		GoRoutineMap:        make(map[string]bool),
		GoRoutineCreatorMap: make(map[string]*GoRoutineCreator),

		SSAFuncMap: make(map[*ssa.Function]bool),

		FilePath: filePath,
	}

	linter, err := linter.ParseSSAFunctions()
	if err != nil {
		return nil, err
	}

	return linter.ParseGlobalElements(), nil
}

func CopyLockSet(lockSet map[string]bool) map[string]bool {
	lockSetCopy := make(map[string]bool)
	for lockID := range lockSet {
		lockSetCopy[lockID] = true
	}
	return lockSetCopy
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

func (l *Linter) ParseSSAFunctions() (*Linter, error) {
	pkgCfg := &packages.Config{Mode: packages.LoadAllSyntax}
	pkgs, err := packages.Load(pkgCfg, l.FilePath)
	if err != nil {
		return nil, err
	}

	prog, _ := ssautil.AllPackages(pkgs, 0)
	prog.Build()
	l.SSAFuncMap = ssautil.AllFunctions(prog)

	return l, nil
}

func (l *Linter) SearchVarID(varAccessID string) string {
	for {
		newVarAccessID, ok := l.GlobalVarMap[varAccessID]
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

func (l *Linter) ParseGlobalElements() *Linter {
	for ssaFunc := range l.SSAFuncMap {
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

					l.GlobalVarMap[idPrefix+"."+varName] = ""
					l.GlobalVarMap[idPrefix+"."+instrName] = idPrefix + "." + varName
				}

				if makeClosureInstr, ok := instr.(*ssa.MakeClosure); ok {
					closureName := makeClosureInstr.Fn.Name()
					closureID := fmt.Sprintf("%s.%s.%d", pkgID, closureName, 0)

					for _, bind := range makeClosureInstr.Bindings {
						bindVarAccessID := idPrefix + "." + bind.Name()
						bindVarID := l.SearchVarID(bindVarAccessID)
						bindVarIDParts := strings.Split(bindVarID, ".")
						bindVarName := bindVarIDParts[3]

						l.GlobalVarMap[closureID+"."+bindVarName] = bindVarID
					}
				}

				if goInstr, ok := instr.(*ssa.Go); ok {
					goRoutineID := goInstr.Call.StaticCallee().String()
					l.GoRoutineMap[goRoutineID] = true
					l.GoRoutineCreatorMap[goInstr.Call.StaticCallee().String()] = &GoRoutineCreator{
						PkgID:   pkgID,
						FuncID:  funcID,
						BlockID: blockID,
						Pos:     int(goInstr.Pos()),
					}
				}
			}
		}
	}

	return l
}

func (l *Linter) IsGoRoutine(fullFuncID string) bool {
	_, ok := l.GoRoutineMap[fullFuncID]
	return ok
}

func (l *Linter) HasVarDataRace(varAccessInfo, sharedVarAccessInfo *VarAccessInfo) bool {
	varAccessFuncID := fmt.Sprintf("%s.%s", varAccessInfo.PkgID, varAccessInfo.FuncID)
	sharedVarAccessFuncID := fmt.Sprintf("%s.%s", sharedVarAccessInfo.PkgID, sharedVarAccessInfo.FuncID)

	if varAccessFuncID == sharedVarAccessFuncID {
		return false
	}

	isVarAccessFuncGR := l.IsGoRoutine(varAccessFuncID)
	isSharedVarAccessFuncGR := l.IsGoRoutine(sharedVarAccessFuncID)

	if !isVarAccessFuncGR && !isSharedVarAccessFuncGR {
		return false
	}

	if isVarAccessFuncGR {
		if varAccessFuncCreator, ok := l.GoRoutineCreatorMap[varAccessFuncID]; ok {
			varAccessFuncCreatorFuncID := fmt.Sprintf("%s.%s", varAccessFuncCreator.PkgID, varAccessFuncCreator.FuncID)
			if varAccessFuncCreatorFuncID == sharedVarAccessFuncID {
				if sharedVarAccessInfo.Pos < varAccessFuncCreator.Pos {
					return false
				}
			}
		}
	}
	if isSharedVarAccessFuncGR {
		if sharedVarAccessFuncCreator, ok := l.GoRoutineCreatorMap[sharedVarAccessFuncID]; ok {
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

func (l *Linter) Analysis() {
	for ssaFunc := range l.SSAFuncMap {
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
							lockID := l.SearchVarID(localLockID)

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
					varID := l.SearchVarID(idPrefix + "." + storeInstr.Addr.Name())

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

					if sharedVarAccessList, ok := l.SharedVarAccessMap[varID]; ok {
						for _, sharedVarAccess := range sharedVarAccessList {
							if l.HasVarDataRace(varAccessInfo, sharedVarAccess) {
								ReportPotentialDataRace(varAccessInfo, sharedVarAccess)
							}
						}
					}

					l.SharedVarAccessMap[varID] = append(l.SharedVarAccessMap[varID], varAccessInfo)
				}

				if unOpInstr, ok := instr.(*ssa.UnOp); ok {
					if unOpInstr.Op.IsOperator() {
						if unOpInstr.Op.String() == "*" {
							varID := l.SearchVarID(idPrefix + "." + unOpInstr.X.Name())

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

							if sharedVarAccessList, ok := l.SharedVarAccessMap[varID]; ok {
								for _, sharedVarAccess := range sharedVarAccessList {
									if l.HasVarDataRace(varAccessInfo, sharedVarAccess) {
										ReportPotentialDataRace(varAccessInfo, sharedVarAccess)
									}
								}
							}

							l.SharedVarAccessMap[varID] = append(l.SharedVarAccessMap[varID], varAccessInfo)
						}
					}
				}
			}
		}
	}
}
